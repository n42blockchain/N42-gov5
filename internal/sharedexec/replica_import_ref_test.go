package sharedexec_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func TestMetadataReferenceImportPreservesAvailabilityAndQC(t *testing.T) {
	ctx := context.Background()
	fixture := sharedexec.NewExecutionFixtureForTest(t)
	server, err := sharedexec.New(fixture.Chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "owner.sock")
	httpServer, listener, err := server.ListenUnix(socket)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpServer.Handler
	var full, refs atomic.Uint64
	var rejectReference atomic.Bool
	httpServer.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/import" {
			full.Add(1)
		}
		if r.URL.Path == "/v1/import-ref" {
			refs.Add(1)
			if rejectReference.Load() {
				http.Error(w, "reference service unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		handler.ServeHTTP(w, r)
	})
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() { httpServer.Close(); server.Close() })
	leader := sharedexec.NewClient(socket, server.Identity())
	defer leader.Close()
	b := signedFixtureBlock(t, fixture, leader)
	raw, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	// There is a Build cache entry, but no sealed import job. The first replica
	// must receive need_body and upload once; the remaining replicas join it.
	replicas := make([]*sharedvalidator.Replica, 7)
	for i := range replicas {
		client := sharedexec.NewClient(socket, server.Identity())
		t.Cleanup(client.Close)
		db := memdb.New(t.TempDir())
		t.Cleanup(db.Close)
		replica, err := sharedvalidator.New(ctx, db, fixture.Chain.Config(), fixture.Chain.GenesisBlock().(*block.Block), fixture.Public, client, sharedvalidator.Options{ImportByReference: true})
		if err != nil {
			t.Fatal(err)
		}
		result, err := replica.Import(ctx, raw)
		if err != nil || !result.Durable {
			t.Fatalf("reference import: %+v %v", result, err)
		}
		if !replica.BlockApplied(b.Hash(), 1) {
			t.Fatal("reference lacks actual bound evidence")
		}
		if err := db.View(ctx, func(tx kv.Tx) error {
			stored, err := rawdb.ReadBlockByHash(tx, b.Hash())
			if err != nil {
				return err
			}
			if stored == nil || len(stored.Transactions()) != 1 {
				t.Fatal("reference skipped local body availability")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		assertNoExecutionState(t, db)
		replicas[i] = replica
	}
	if full.Load() != 1 || refs.Load() != 7 || server.ImportCalls() != 1 {
		t.Fatalf("RPC/actual import counts full=%d refs=%d executed=%d", full.Load(), refs.Load(), server.ImportCalls())
	}
	// Existing callers without runtime options keep the full-body transport.
	defaultDB := memdb.New(t.TempDir())
	t.Cleanup(defaultDB.Close)
	defaultClient := sharedexec.NewClient(socket, server.Identity())
	t.Cleanup(defaultClient.Close)
	defaultReplica, err := sharedvalidator.New(ctx, defaultDB, fixture.Chain.Config(), fixture.Chain.GenesisBlock().(*block.Block), fixture.Public, defaultClient)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := defaultReplica.Import(ctx, raw); err != nil || !result.Durable {
		t.Fatalf("default full-body transport: %+v %v", result, err)
	}
	if full.Load() != 2 || refs.Load() != 7 || server.ImportCalls() != 1 {
		t.Fatal("default transport changed or repeated actual execution")
	}
	// A transport failure is not need_body and cannot silently fall back or
	// create execution evidence, even though the complete body was retained.
	rejectReference.Store(true)
	failureDB := memdb.New(t.TempDir())
	t.Cleanup(failureDB.Close)
	failureReplica, err := sharedvalidator.New(ctx, failureDB, fixture.Chain.Config(), fixture.Chain.GenesisBlock().(*block.Block), fixture.Public, defaultClient, sharedvalidator.Options{ImportByReference: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failureReplica.Import(ctx, raw); err == nil {
		t.Fatal("transport rejection silently fell back")
	}
	rejectReference.Store(false)
	if full.Load() != 2 || refs.Load() != 8 || failureReplica.BlockApplied(b.Hash(), 1) {
		t.Fatal("failed reference uploaded a body or produced execution evidence")
	}
	if err := replicas[0].CommitCertificate(ctx, replicaQC(fixture.Keys, b.Hash(), 4)); err == nil {
		t.Fatal("four votes finalized reference payload")
	}
	qc := replicaQC(fixture.Keys, b.Hash(), 5)
	for _, replica := range replicas {
		if err := replica.CommitCertificate(ctx, qc); err != nil {
			t.Fatal(err)
		}
		progress, err := replica.CanonicalProgress(ctx)
		if err != nil || progress.CommittedExecutedTransactions != 1 || progress.BlockHash != b.Hash() {
			t.Fatal("QC did not advance exact unique count")
		}
	}
}
