// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// TestQMDBAbortedFlushPeelsAndRecommits reproduces the commit-transactionality
// gap: the live write path used to consume the undo, adopt the flush cursor and
// evict the flushed window INSIDE the block's MDBX tx — a failure after that
// point rolled the tx back but left the in-memory computer mutated (no undo to
// peel with, a cursor pointing past rows that never reached disk, evicted
// entries recoverable from nowhere). With the staged-flush contract the
// failure path is: rollback → AbortFlushed → peel → the next block flushes the
// SAME rows again, and the store ends byte-identical to one that never saw the
// failure.
func TestQMDBAbortedFlushPeelsAndRecommits(t *testing.T) {
	prevCfg := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevCfg })

	mkBlock := func(base, n int, nonce uint64) map[types.Address]*account.StateAccount {
		accts := make(map[types.Address]*account.StateAccount, n)
		for i := 0; i < n; i++ {
			accts[rlAddr(base+i)] = qmAcct(nonce, uint64(1000+base+i))
		}
		return accts
	}

	db := memdb.NewTestDB(t)
	rc := NewQMDBRootComputer()
	rc.EnableUndoRecording()

	// Two committed blocks; block 2 overwrites half of block 1 so flushed slots
	// die (populating deadFlushed — the staged dead-row reclaim path).
	rlApplyBlock(t, db, rc, mkBlock(0, 1200, 1))
	_, root2 := rlApplyBlock(t, db, rc, mkBlock(600, 1200, 2))

	// Failed block 3a: ComputeRoot + FlushTo inside a tx that then ROLLS BACK
	// (a failure between the QMDB flush and the tx commit — WriteQMDBUndo, a
	// later table write, or the commit itself).
	tx, err := db.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rc.SetCold(tx)
	if _, err := rc.ComputeRoot(mkBlock(300, 900, 3), nil); err != nil {
		t.Fatalf("ComputeRoot(3a): %v", err)
	}
	if _, err := rc.FlushTo(tx); err != nil {
		t.Fatalf("FlushTo(3a): %v", err)
	}
	tx.Rollback()
	rc.SetCold(nil)

	// The recovery contract: discard the staged flush, then peel the failed
	// block's appends (revertUncommittedQMDBAppends in the live path).
	rc.AbortFlushed()
	undo := rc.TakeUndo()
	if undo == nil {
		t.Fatal("undo record must survive a post-flush failure — it is the only way to peel the dangling appends")
	}
	if err := db.View(t.Context(), func(vtx kv.Tx) error {
		rc.SetCold(vtx)
		defer rc.SetCold(nil)
		return rc.Tree().ApplyUndo(undo)
	}); err != nil {
		t.Fatalf("peel: %v", err)
	}
	if got := rc.Root(); got != root2 {
		t.Fatalf("peeled root=%x want block-2 root %x", got[:8], root2[:8])
	}

	// Block 3b (the block that actually wins) commits through the normal path.
	_, root3b := rlApplyBlock(t, db, rc, mkBlock(300, 900, 4))

	// The store must be indistinguishable from one that never saw 3a: a fresh
	// computer reloading from disk reproduces 3b's root (any row skipped by a
	// wrongly-adopted cursor, or a live row deleted by a stale dead-slot
	// reclaim, shifts it).
	fresh := NewQMDBRootComputer()
	if err := db.Update(t.Context(), func(utx kv.RwTx) error {
		fresh.SetCold(utx)
		defer fresh.SetCold(nil)
		return fresh.LoadFrom(utx)
	}); err != nil {
		t.Fatalf("fresh LoadFrom: %v", err)
	}
	if fresh.Root() != root3b {
		t.Fatalf("reloaded root=%x want %x — the aborted flush leaked into the persisted layout",
			fresh.Root().Bytes()[:8], root3b[:8])
	}

	// Cross-check against a computer that never experienced the failure.
	ctlDB := memdb.NewTestDB(t)
	ctl := NewQMDBRootComputer()
	ctl.EnableUndoRecording()
	rlApplyBlock(t, ctlDB, ctl, mkBlock(0, 1200, 1))
	rlApplyBlock(t, ctlDB, ctl, mkBlock(600, 1200, 2))
	_, ctlRoot := rlApplyBlock(t, ctlDB, ctl, mkBlock(300, 900, 4))
	if ctlRoot != root3b {
		t.Fatalf("failure-recovery root=%x diverges from the never-failed control %x", root3b[:8], ctlRoot[:8])
	}
}

// TestQMDBUncommittedFlushIsRetried pins the staged-cursor semantics: until
// CommitFlushed adopts the cursor, a re-flush covers the same slots again (the
// rolled-back tx threw the first copy away), and after CommitFlushed the next
// flush is incremental.
func TestQMDBUncommittedFlushIsRetried(t *testing.T) {
	prevCfg := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevCfg })

	db := memdb.NewTestDB(t)
	rc := NewQMDBRootComputer()
	rc.EnableUndoRecording()

	accts := make(map[types.Address]*account.StateAccount, 64)
	for i := 0; i < 64; i++ {
		accts[rlAddr(i)] = qmAcct(1, uint64(1000+i))
	}

	tx, err := db.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	rc.SetCold(tx)
	defer rc.SetCold(nil)
	if _, err := rc.ComputeRoot(accts, nil); err != nil {
		t.Fatal(err)
	}
	first, err := rc.FlushTo(tx)
	if err != nil {
		t.Fatal(err)
	}
	// Not committed: the retry must rewrite the same entry log.
	retry, err := rc.FlushTo(tx)
	if err != nil {
		t.Fatal(err)
	}
	if retry < first {
		t.Fatalf("uncommitted retry wrote %dB < first flush %dB — entries were skipped", retry, first)
	}
	rc.CommitFlushed()
	// Committed: nothing new appended, so the incremental flush writes only meta.
	incr, err := rc.FlushTo(tx)
	if err != nil {
		t.Fatal(err)
	}
	if incr >= first {
		t.Fatalf("post-commit incremental flush wrote %dB (full flush was %dB) — cursor was not adopted", incr, first)
	}
}
