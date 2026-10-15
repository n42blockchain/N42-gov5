package sharedexec_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func TestRemoteProducerSignsLocallyAndReusesOwnCandidateWAL(t *testing.T) {
	ctx := context.Background()
	fixture := sharedexec.NewExecutionFixtureForTest(t)
	server, err := sharedexec.New(fixture.Chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	client := sharedexec.NewClient(sharedexec.StartUnixExecutionForTest(t, server), server.Identity())
	defer client.Close()
	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	replica, err := sharedvalidator.New(ctx, db, fixture.Chain.Config(), fixture.Chain.GenesisBlock().(*block.Block), fixture.Leader, client)
	if err != nil {
		t.Fatal(err)
	}
	service := hotstuff.NewService(fixture.Leader, nil, db, "unused", "unused")
	defer service.Stop()
	if err := replica.AttachService(service, replicaTestFetcher{requested: make(chan types.Hash, 8)}); err != nil {
		t.Fatal(err)
	}
	producer, err := sharedvalidator.NewProducer(ctx, replica, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Stop()
	if err := producer.SubmitTransactions(fixture.Template.Transactions()); err != nil {
		t.Fatal(err)
	}
	b, err := producer.Produce(ctx, fixture.Chain.GenesisBlock().Hash())
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Transactions()) != 1 || b.Header().(*block.Header).GasUsed != 21000 || !replica.BlockApplied(b.Hash(), 1) {
		t.Fatal("producer did not execute signed transfer through remote gate")
	}
	if replica.CurrentBlock().Hash() != fixture.Chain.GenesisBlock().Hash() {
		t.Fatal("proposal was treated as canonical")
	}
	_, ownerKey := fixture.Public.SignerKey()
	if ownerKey != nil {
		t.Fatal("execution service acquired leader private key")
	}
	if err := producer.CommitToCanonical(b.Hash()); err == nil {
		t.Fatal("hash-only producer commit bypassed QC")
	}
	var viewKey [8]byte
	viewKey[7] = 1
	key := append([]byte("performance-own-candidate/"), viewKey[:]...)
	if err := db.View(ctx, func(tx kv.Tx) error {
		raw, err := tx.GetOne(modules.HotStuffState, key)
		if err != nil {
			return err
		}
		if len(raw) == 0 {
			t.Fatal("proposal emitted before durable own-candidate binding")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	proposalSeen := false
	for !proposalSeen {
		select {
		case output := <-fixture.Leader.OutputCh():
			if output.Message != nil {
				if proposal, ok := output.Message.Payload.(*hotstuff.Proposal); ok {
					if proposal.BlockHash != b.Hash() {
						t.Fatal("engine proposed another payload")
					}
					proposalSeen = true
				}
			}
		default:
			t.Fatal("producer never notified real consensus engine")
		}
	}
	// A new producer object has an empty pending pool. It must reuse the
	// durable sealed body for this view instead of building an empty sibling.
	restarted, err := sharedvalidator.NewProducer(ctx, replica, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Stop()
	again, err := restarted.Produce(ctx, fixture.Chain.GenesisBlock().Hash())
	if err != nil || again.Hash() != b.Hash() {
		t.Fatalf("own-candidate WAL retry: %v", err)
	}
	if server.BuildCalls() != 1 || server.ImportCalls() != 1 {
		t.Fatal("WAL retry rebuilt or re-executed")
	}
	if err := service.CommitCertificate(ctx, replicaQC(fixture.Keys, b.Hash(), 5)); err != nil {
		t.Fatal(err)
	}
	if replica.CurrentBlock().Hash() != b.Hash() {
		t.Fatal("QC did not finalize remote-produced block")
	}
	assertNoExecutionState(t, db)
}

func TestRemoteProducerRejectsObserverAndUnsignedAdmission(t *testing.T) {
	ctx := context.Background()
	fixture := sharedexec.NewExecutionFixtureForTest(t)
	server, err := sharedexec.New(fixture.Chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	client := sharedexec.NewClient(sharedexec.StartUnixExecutionForTest(t, server), server.Identity())
	defer client.Close()
	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	replica, err := sharedvalidator.New(ctx, db, fixture.Chain.Config(), fixture.Chain.GenesisBlock().(*block.Block), fixture.Public, client)
	if err != nil {
		t.Fatal(err)
	}
	service := hotstuff.NewService(fixture.Public, nil, db, "unused", "unused")
	defer service.Stop()
	if err := replica.AttachService(service, replicaTestFetcher{requested: make(chan types.Hash, 8)}); err != nil {
		t.Fatal(err)
	}
	producer, err := sharedvalidator.NewProducer(ctx, replica, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Stop()
	to := types.Address{9}
	unsigned := transaction.NewTx(&transaction.LegacyTx{Gas: 21000, GasPrice: uint256.NewInt(1), To: &to, Value: uint256.NewInt(1)})
	if err := producer.SubmitTransactions([]*transaction.Transaction{unsigned}); err == nil {
		t.Fatal("unsigned transaction admitted")
	}
	if _, err := producer.Produce(ctx, fixture.Chain.GenesisBlock().Hash()); err == nil {
		t.Fatal("public executor/observer produced a signed block")
	}
	if server.BuildCalls() != 0 || server.ImportCalls() != 0 {
		t.Fatal("nonleader initiated execution")
	}
}

func TestRemoteProducerStopCancelsBlockedBuildBeforeProposal(t *testing.T) {
	ctx := context.Background()
	fixture := sharedexec.NewExecutionFixtureForTest(t)
	server, err := sharedexec.New(fixture.Chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "execution.sock")
	httpServer, listener, err := server.ListenUnix(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer httpServer.Close()
	entered := make(chan struct{})
	httpServer.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/build" {
			_, _ = io.Copy(io.Discard, r.Body)
			_ = r.Body.Close()
			close(entered)
			<-r.Context().Done()
			return
		}
		server.ServeHTTP(w, r)
	})
	go func() { _ = httpServer.Serve(listener) }()
	client := sharedexec.NewClient(socket, server.Identity())
	defer client.Close()
	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	replica, err := sharedvalidator.New(ctx, db, fixture.Chain.Config(), fixture.Chain.GenesisBlock().(*block.Block), fixture.Leader, client)
	if err != nil {
		t.Fatal(err)
	}
	service := hotstuff.NewService(fixture.Leader, nil, db, "unused", "unused")
	defer service.Stop()
	if err := replica.AttachService(service, replicaTestFetcher{requested: make(chan types.Hash, 8)}); err != nil {
		t.Fatal(err)
	}
	producer, err := sharedvalidator.NewProducer(ctx, replica, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Stop()
	if err := producer.SubmitTransactions(fixture.Template.Transactions()); err != nil {
		t.Fatal(err)
	}
	if err := producer.Start(); err != nil {
		t.Fatal(err)
	}
	producer.TriggerBlockProduction(fixture.Chain.GenesisBlock().Hash())
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("async producer did not reach real Unix build transport")
	}
	done := make(chan struct{})
	go func() { producer.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("producer Stop did not cancel/drain active build")
	}
	if server.BuildCalls() != 0 || server.ImportCalls() != 0 {
		t.Fatal("canceled request published/executed a candidate")
	}
	if err := producer.SubmitTransactions(fixture.Template.Transactions()); err == nil {
		t.Fatal("closed producer admitted transactions")
	}
}
