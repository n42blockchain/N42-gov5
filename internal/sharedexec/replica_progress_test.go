package sharedexec_test

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func TestCanonicalProgressCountsOnlyDurablyCommittedTransactions(t *testing.T) {
	ctx := context.Background()
	f := sharedexec.NewExecutionFixtureForTest(t)
	server, err := sharedexec.New(f.Chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	client := sharedexec.NewClient(sharedexec.StartUnixExecutionForTest(t, server), server.Identity())
	defer client.Close()
	db := memdb.New(t.TempDir())
	defer db.Close()
	genesis := f.Chain.GenesisBlock().(*block.Block)
	r, err := sharedvalidator.New(ctx, db, f.Chain.Config(), genesis, f.Public, client)
	if err != nil {
		t.Fatal(err)
	}
	check := func(number, count uint64) {
		t.Helper()
		progress, err := r.CanonicalProgress(ctx)
		if err != nil || progress.Identity != client.Identity() || progress.Number != number || progress.CommittedExecutedTransactions != count || progress.BlockHash != r.CurrentBlock().Hash() {
			t.Fatalf("canonical progress: %+v %v", progress, err)
		}
	}
	check(0, 0)
	b := signedFixtureBlock(t, f, client)
	check(0, 0) // Candidate Build is not a committed transaction.
	raw, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if result, err := r.Import(ctx, raw); err != nil || !result.Durable {
		t.Fatalf("real import: %+v %v", result, err)
	}
	check(0, 0) // Durable execution alone is not finality.
	if err := r.CommitCertificate(ctx, replicaQC(f.Keys, b.Hash(), 4)); err == nil {
		t.Fatal("four votes were counted")
	}
	check(0, 0)
	qc := replicaQC(f.Keys, b.Hash(), 5)
	if err := r.CommitCertificate(ctx, qc); err != nil {
		t.Fatal(err)
	}
	check(1, 1) // Excludes the body's two reserved system positions.
	if err := r.CommitCertificate(ctx, qc); err != nil {
		t.Fatal(err)
	}
	check(1, 1)
	if err := sharedvalidator.InitializeMetadata(ctx, db, f.Chain.Config(), genesis); err != nil {
		t.Fatal(err)
	}
	check(1, 1)
	// A bounded migration of the pre-counter prototype requires real bound
	// execution evidence for each existing canonical body, never defaults to 0.
	key := []byte("performance-canonical-progress-v1")
	if err := db.Update(ctx, func(tx kv.RwTx) error { return tx.Delete(modules.HotStuffState, key) }); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CanonicalProgress(ctx); err == nil {
		t.Fatal("missing counter was reported as zero")
	}
	if err := sharedvalidator.InitializeMetadata(ctx, db, f.Chain.Config(), genesis); err != nil {
		t.Fatal(err)
	}
	check(1, 1)
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		if err := tx.Delete(modules.HotStuffState, key); err != nil {
			return err
		}
		evidence := append([]byte("performance-remote-executed/"), b.Hash().Bytes()...)
		return tx.Delete(modules.HotStuffState, evidence)
	}); err != nil {
		t.Fatal(err)
	}
	if err := sharedvalidator.InitializeMetadata(ctx, db, f.Chain.Config(), genesis); err == nil {
		t.Fatal("backfill fabricated execution evidence")
	}
	if _, err := r.CanonicalProgress(ctx); err == nil {
		t.Fatal("failed backfill left a misleading progress record")
	}
}
