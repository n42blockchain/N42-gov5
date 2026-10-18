package sharedexec_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// Real owner execution, Unix RPC, durable metadata and public 5-of-7 CommitQC:
// no nonce/marker edits and no bypass of sealed-header or body verification.
func TestRecoveryOnlyReproposesAppliedChildAndWALWithoutFreshBuild(t *testing.T) {
	for _, wal := range []bool{false, true} {
		t.Run(map[bool]string{false: "owner-applied-child", true: "own-wal"}[wal], func(t *testing.T) {
			ctx := context.Background()
			f, client, db, b := startupReplicaFixture(t, false)
			if !wal {
				db = memdb.New(t.TempDir())
				t.Cleanup(db.Close)
			}
			replica, service, producer := strictQueueReplica(t, f, client, db)
			if err := producer.UseOwnerQueue(ctx, sharedvalidator.OwnerQueueOptions{MaxTransactions: 6, MaxEncodedBytes: 1 << 20}); err != nil {
				t.Fatal(err)
			}
			api, err := sharedvalidator.NewAPI(replica, producer)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			socket := filepath.Join(dir, "validator.sock")
			server, listener, err := api.ListenUnix(socket)
			if err != nil {
				t.Fatal(err)
			}
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(func() { _ = server.Close(); api.Stop() })
			control := sharedvalidator.NewAPIClient(socket, client.Identity())
			t.Cleanup(control.Close)
			ack, err := control.EnterRecoveryOnly(ctx, *producer.OwnerQueueBinding())
			if err != nil || !ack.DrainAcknowledged {
				t.Fatalf("Unix recovery-only ACK: %+v %v", ack, err)
			}
			state, err := producer.EnterRecoveryOnly(ctx)
			if err != nil || !state.DrainAcknowledged || state.FreshBuilds != 0 {
				t.Fatalf("startup barrier %+v %v", state, err)
			}
			adopted, err := producer.Produce(ctx, f.Chain.GenesisBlock().Hash())
			if err != nil || adopted == nil || adopted.Hash() != b.Hash() {
				t.Fatalf("retained candidate not recovered: %v", err)
			}
			if err := service.CommitCertificate(ctx, replicaQC(f.Keys, b.Hash(), 4)); err == nil {
				t.Fatal("four votes finalized recovery")
			}
			if err := service.CommitCertificate(ctx, replicaQC(f.Keys, b.Hash(), 5)); err != nil {
				t.Fatal(err)
			}
			after, err := f.Server.StrictIngressStatus()
			progress, perr := replica.CanonicalProgress(ctx)
			if err != nil || perr != nil || after.Reconciling || after.CanonicalHash != b.Hash() || progress.BlockHash != b.Hash() || f.Server.BuildCalls() != 0 || f.Server.ImportCalls() != 0 {
				t.Fatalf("not canonical without reexecution: %+v %v %v", after, err, perr)
			}
			// Same view/parent still reuses the bound WAL, never opens a fresh build.
			if _, err := producer.Produce(ctx, f.Chain.GenesisBlock().Hash()); err != nil {
				t.Fatal(err)
			}
			next, err := producer.EnterRecoveryOnly(ctx)
			if err != nil || next != state {
				t.Fatalf("recovery changed fresh admission counters: %+v %v", next, err)
			}
			assertNoExecutionState(t, db)
		})
	}
}

func TestRecoveryOnlyWithoutCandidateRejectsFreshEmptyBuild(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryOnlyFixture(t)
	_, err := f.producer.Produce(ctx, f.fixture.Chain.GenesisBlock().Hash())
	if !errors.Is(err, sharedvalidator.ErrRecoveryOnly) {
		t.Fatalf("fresh empty build not refused: %v", err)
	}
	if f.fixture.Server.BuildCalls() != 0 || f.fixture.Server.ImportCalls() != 0 {
		t.Fatal("recovery executed a fresh empty block")
	}
}

type recoveryOnlyFixture struct {
	fixture  *sharedexec.StrictIngressFixtureForTest
	producer *sharedvalidator.Producer
}

func newRecoveryOnlyFixture(t *testing.T) recoveryOnlyFixture {
	t.Helper()
	f := sharedexec.NewStrictIngressFixtureForTest(t)
	client := sharedexec.NewClient(sharedexec.StartUnixExecutionForTest(t, f.Server), f.Server.Identity())
	t.Cleanup(client.Close)
	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	_, _, producer := strictQueueReplica(t, f, client, db)
	if err := producer.UseOwnerQueue(context.Background(), sharedvalidator.OwnerQueueOptions{MaxTransactions: 6, MaxEncodedBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := producer.EnterRecoveryOnly(context.Background()); err != nil {
		t.Fatal(err)
	}
	return recoveryOnlyFixture{f, producer}
}

func TestRecoveryOnlyWaitsForInFlightQueueRPCBeforeAcknowledgement(t *testing.T) {
	f := newBlockedQueueProducer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	produced := make(chan error, 1)
	go func() { _, err := f.producer.Produce(ctx, f.fixture.Chain.GenesisBlock().Hash()); produced <- err }()
	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("build did not enter actual Unix RPC")
	}
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelDrain()
	if _, err := f.producer.EnterRecoveryOnly(drainCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("barrier acknowledged blocked Produce: %v", err)
	}
	cancel()
	select {
	case err := <-produced:
		if err == nil {
			t.Fatal("canceled build proposed")
		}
	case <-time.After(time.Second):
		t.Fatal("Produce did not return")
	}
	state, err := f.producer.EnterRecoveryOnly(context.Background())
	if err != nil || !state.DrainAcknowledged || state.FreshBuilds != 1 {
		t.Fatalf("join did not acknowledge returned RPC: %+v %v", state, err)
	}
	if _, err := f.producer.Produce(context.Background(), f.fixture.Chain.GenesisBlock().Hash()); !errors.Is(err, sharedvalidator.ErrRecoveryOnly) {
		t.Fatalf("fresh retry after ACK: %v", err)
	}
	if f.fixture.Server.BuildCalls() != 0 || f.fixture.Server.ImportCalls() != 0 {
		t.Fatal("blocked request executed after acknowledgement")
	}
}
