package sharedexec_test

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

func TestNextLeaderReproposesExecutedOrphanWithoutBuildOrImport(t *testing.T) {
	ctx := context.Background()
	f := sharedexec.NewExecutionFixtureForTest(t)
	server, err := sharedexec.New(f.Chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	client := sharedexec.NewClient(sharedexec.StartUnixExecutionForTest(t, server), server.Identity())
	defer client.Close()
	request := sharedexec.AppliedChildRequest{Identity: client.Identity(), ParentHash: f.Chain.GenesisBlock().Hash(), Number: 1, View: 2}
	empty, err := client.AppliedChild(ctx, request)
	if err != nil || len(empty.BlockRLP) != 0 || empty.Executed != nil {
		t.Fatalf("fresh parent: %+v %v", empty, err)
	}
	makeProducer := func(engine *hotstuff.HotStuff) (*sharedvalidator.Replica, *sharedvalidator.Producer) {
		db := memdb.New(t.TempDir())
		t.Cleanup(db.Close)
		replica, err := sharedvalidator.New(ctx, db, f.Chain.Config(), f.Chain.GenesisBlock().(*block.Block), engine, client)
		if err != nil {
			t.Fatal(err)
		}
		service := hotstuff.NewService(engine, nil, db, "unused", "unused")
		t.Cleanup(service.Stop)
		if err := replica.AttachService(service, replicaTestFetcher{requested: make(chan types.Hash, 8)}); err != nil {
			t.Fatal(err)
		}
		p, err := sharedvalidator.NewProducer(ctx, replica, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Stop)
		return replica, p
	}
	_, first := makeProducer(f.Leader)
	if err := first.SubmitTransactions(f.Template.Transactions()); err != nil {
		t.Fatal(err)
	}
	orphan, err := first.Produce(ctx, request.ParentHash)
	if err != nil {
		t.Fatal(err)
	}
	if f.Chain.CurrentBlock().Hash() != request.ParentHash {
		t.Fatal("orphan was finalized without QC")
	}
	// No vote/QC is delivered. A different, independently keyed leader starts
	// view two with only genesis metadata and an empty transaction queue.
	cfg := f.Chain.Config()
	secondEngine := hotstuff.New(cfg.HotStuff, cfg)
	defer secondEngine.Close()
	set := f.Public.Engine().ValidatorSetForView(2)
	index := hotstuff.LeaderForView(2, set)
	address, err := set.GetAddress(index)
	if err != nil {
		t.Fatal(err)
	}
	if secondEngine.IsCurrentLeader() {
		t.Fatal("uninitialized engine is leader")
	}
	secondEngine.Authorize(address, f.Keys[int(index)])
	if err := secondEngine.InitEngineFromConfig(); err != nil {
		t.Fatal(err)
	}
	secondEngine.Engine().RestoreState(2, hotstuff.GenesisQC(), hotstuff.GenesisQC(), 0)
	secondReplica, second := makeProducer(secondEngine)
	adopted, err := second.Produce(ctx, request.ParentHash)
	if err != nil || adopted.Hash() != orphan.Hash() {
		t.Fatalf("next leader failed to reuse orphan: %v", err)
	}
	if server.BuildCalls() != 1 || server.ImportCalls() != 1 {
		t.Fatalf("reproposal re-executed: build=%d import=%d", server.BuildCalls(), server.ImportCalls())
	}
	seen := false
	for !seen {
		select {
		case output := <-secondEngine.OutputCh():
			if output.Message != nil {
				if proposal, ok := output.Message.Payload.(*hotstuff.Proposal); ok {
					if proposal.View != 2 || proposal.BlockHash != orphan.Hash() || proposal.Proposer != index {
						t.Fatal("new leader proposed wrong view/payload")
					}
					seen = true
				}
			}
		default:
			t.Fatal("new leader did not emit actual consensus Proposal")
		}
	}
	got, err := client.AppliedChild(ctx, request)
	if err != nil || got.Executed == nil || got.Executed.BlockHash != orphan.Hash() {
		t.Fatalf("durable child query: %v", err)
	}
	wrong := request
	wrong.Identity.ChainID = "999"
	if _, err := server.AppliedChild(ctx, wrong); err == nil {
		t.Fatal("wrong full chain identity accepted")
	}
	wrong = request
	wrong.ParentHash = types.Hash{0xff}
	if _, err := client.AppliedChild(ctx, wrong); err == nil {
		t.Fatal("off-lineage parent accepted")
	}
	wrong = request
	wrong.Number = 3
	if _, err := client.AppliedChild(ctx, wrong); err == nil {
		t.Fatal("missing parent height accepted")
	}
	qc := replicaQC(f.Keys, orphan.Hash(), 5)
	qc.View = 2
	var signatures []blscommon.Signature
	for i := 0; i < 5; i++ {
		signatures = append(signatures, f.Keys[i].Sign(hotstuff.CommitSigningMessage(2, orphan.Hash())))
	}
	qc.AggregateSignature = bls.AggregateSignatures(signatures).Marshal()
	if err := secondReplica.CommitCertificate(ctx, qc); err != nil {
		t.Fatal("new-view CommitQC did not finalize re-proposed payload:", err)
	}
	if secondReplica.CurrentBlock().Hash() != orphan.Hash() || f.Chain.CurrentBlock().Hash() != orphan.Hash() {
		t.Fatal("re-proposal QC failed to advance owner and validator canonical heads")
	}
	// A fresh service has no import/build registry entries. Child recovery
	// must come from the actual database execution evidence, not that cache.
	server.Close()
	reopened, err := sharedexec.New(f.Chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	reopenedClient := sharedexec.NewClient(sharedexec.StartUnixExecutionForTest(t, reopened), reopened.Identity())
	defer reopenedClient.Close()
	recovered, err := reopenedClient.AppliedChild(ctx, request)
	if err != nil || recovered.Executed == nil || recovered.Executed.BlockHash != orphan.Hash() || reopened.ImportCalls() != 0 || reopened.BuildCalls() != 0 {
		t.Fatalf("registry-free durable recovery: %+v %v", recovered.Executed, err)
	}
}
