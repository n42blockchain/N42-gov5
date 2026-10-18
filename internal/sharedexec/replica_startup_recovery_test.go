package sharedexec_test

import (
	"context"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

func startupReplicaFixture(t *testing.T, pending bool) (*sharedexec.StrictIngressFixtureForTest, *sharedexec.Client, kv.RwDB, *block.Block) {
	t.Helper()
	ctx := context.Background()
	f := sharedexec.NewStrictIngressFixtureForTest(t)
	oldClient := sharedexec.NewClient(sharedexec.StartUnixExecutionForTest(t, f.Server), f.Server.Identity())
	t.Cleanup(oldClient.Close)
	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	_, service, producer := strictQueueReplica(t, f, oldClient, db)
	// Prepare a historical retained empty candidate through ordinary production.
	// Reopened producers below still bind owner-queue recovery before use.
	b, err := producer.Produce(ctx, f.Chain.GenesisBlock().Hash())
	if err != nil {
		t.Fatal(err)
	}
	f.Server.Close()
	if pending {
		if err := service.CommitCertificate(ctx, replicaQC(f.Keys, b.Hash(), 5)); err == nil {
			t.Fatal("closed owner unexpectedly acknowledged QC")
		}
		certificates, err := service.PendingCommitCertificates(ctx)
		if err != nil || len(certificates) != 1 || certificates[0].BlockHash != b.Hash() {
			t.Fatalf("full pending QC was not retained: %+v %v", certificates, err)
		}
	}
	producer.Stop()
	service.Stop()
	owner, err := sharedexec.New(f.Chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	if err := owner.EnableStrictIngress(sharedexec.StrictIngressOptions{Manifest: f.Manifest, MaxTransactions: 32, MaxEncodedBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	f.Server = owner
	client := sharedexec.NewClient(sharedexec.StartUnixExecutionForTest(t, owner), owner.Identity())
	t.Cleanup(client.Close)
	return f, client, db, b
}

func TestOwnerQueueStartupPinsBeforePendingQCOrAppliedChildRecovery(t *testing.T) {
	for _, pending := range []bool{true, false} {
		name := "existing-child"
		if pending {
			name = "pending-certificate"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f, client, db, b := startupReplicaFixture(t, pending)
			if !pending {
				// A different validator has no own-candidate WAL. It must obtain
				// the already executed child rather than build a new candidate.
				db = memdb.New(t.TempDir())
				t.Cleanup(db.Close)
			}
			replica, service, producer := strictQueueReplica(t, f, client, db)
			before, _ := f.Server.StrictIngressStatus()
			if !before.Reconciling {
				t.Fatal("fixture did not retain applied/canonical gap")
			}
			if err := producer.UseOwnerQueue(ctx, sharedvalidator.OwnerQueueOptions{MaxTransactions: 6, MaxEncodedBytes: 1 << 20}); err != nil {
				t.Fatalf("validator could not start QC recovery: %v", err)
			}
			if pending {
				if err := service.ReplayCommitCertificates(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				adopted, err := producer.Produce(ctx, f.Chain.GenesisBlock().Hash())
				if err != nil || adopted.Hash() != b.Hash() {
					t.Fatalf("existing child did not recover: %v", err)
				}
				if err := service.CommitCertificate(ctx, replicaQC(f.Keys, b.Hash(), 5)); err != nil {
					t.Fatal(err)
				}
			}
			after, _ := f.Server.StrictIngressStatus()
			progress, err := replica.CanonicalProgress(ctx)
			if err != nil || after.Reconciling || after.CanonicalHash != b.Hash() || progress.BlockHash != b.Hash() || after.NewlyAdmitted != 0 || f.Server.BuildCalls() != 0 || f.Server.ImportCalls() != 0 {
				t.Fatalf("QC recovery changed execution/queue or stayed blocked: %+v %+v %v", after, progress, err)
			}
			assertNoExecutionState(t, db)
		})
	}
}

func TestOwnerQueueStartupLockedTipWithoutCommitQCStaysBlocked(t *testing.T) {
	ctx := context.Background()
	f, client, db, b := startupReplicaFixture(t, false)
	cfg := f.Chain.Config()
	engine := hotstuff.New(cfg.HotStuff, cfg)
	t.Cleanup(func() { _ = engine.Close() })
	set := f.Public.Engine().ValidatorSetForView(2)
	index := hotstuff.LeaderForView(2, set)
	address, err := set.GetAddress(index)
	if err != nil {
		t.Fatal(err)
	}
	engine.Authorize(address, f.Keys[int(index)])
	if err := engine.InitEngineFromConfig(); err != nil {
		t.Fatal(err)
	}
	prepare := &hotstuff.QuorumCertificate{View: 1, BlockHash: b.Hash(), Signers: make([]bool, 7)}
	var signatures []blscommon.Signature
	for i := 0; i < 5; i++ {
		prepare.Signers[i] = true
		signatures = append(signatures, f.Keys[i].Sign(hotstuff.SigningMessage(1, b.Hash())))
	}
	prepare.AggregateSignature = bls.AggregateSignatures(signatures).Marshal()
	engine.Engine().RestoreState(2, *prepare, hotstuff.GenesisQC(), 0)
	f.Leader = engine
	_, _, producer := strictQueueReplica(t, f, client, db)
	if err := producer.UseOwnerQueue(ctx, sharedvalidator.OwnerQueueOptions{MaxTransactions: 6, MaxEncodedBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := producer.Produce(ctx, b.Hash()); err == nil || !strings.Contains(err.Error(), "reconciling") {
		t.Fatalf("locked tip without CommitQC was silently recovered: %v", err)
	}
	status, _ := f.Server.StrictIngressStatus()
	if !status.Reconciling || status.CanonicalNumber != 0 || status.NewlyAdmitted != 0 || f.Server.BuildCalls() != 0 || f.Server.ImportCalls() != 0 {
		t.Fatalf("missing certificate bypassed startup gate: %+v", status)
	}
}
