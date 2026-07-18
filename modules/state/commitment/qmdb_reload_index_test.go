// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
)

func rlAddr(i int) types.Address {
	var a types.Address
	a[18], a[19] = byte(i>>8), byte(i)
	return a
}

// rlApplyBlock mirrors the live per-block cadence: cold getter re-pointed at
// the block's tx, ComputeRoot, FlushTo, EvictFlushed, commit. Returns the
// block's undo record.
func rlApplyBlock(t *testing.T, db kv.RwDB, rc *QMDBRootComputer, accts map[types.Address]*account.StateAccount) (*qmdb.BlockUndo, types.Hash) {
	t.Helper()
	tx, err := db.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rc.SetCold(tx)
	root, err := rc.ComputeRoot(accts, nil)
	if err != nil {
		t.Fatalf("ComputeRoot: %v", err)
	}
	if _, err := rc.FlushTo(tx); err != nil {
		t.Fatalf("FlushTo: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rc.CommitFlushed()
	rc.EvictFlushed()
	rc.SetCold(nil)
	return rc.TakeUndo(), root
}

// TestQMDBMidRunReloadRebuildsInRAMIndex reproduces the live recovery path
// that silently corrupted the in-RAM live-key index:
//
//  1. blocks are applied and flushed per-tx (live cadence);
//  2. a revert runs inside a tx that then ROLLS BACK (the mid-loop failure of
//     a multi-block unwind) — the in-memory tree AND index now reflect the
//     revert while disk does not;
//  3. the recovery reload (SetCold + LoadFrom on a fresh tx) restores the
//     tree from disk. Before the fix, Tree.LoadFrom's non-empty-index fast
//     path KEPT the stale post-revert index, so a later overwrite of a key
//     the rolled-back revert had touched deactivated the wrong slot — the
//     node's roots then diverged from a node that never took this path.
//
// The test drives two computers over the same post-recovery block and
// requires identical roots.
func TestQMDBMidRunReloadRebuildsInRAMIndex(t *testing.T) {
	prevCfg := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevCfg })
	db := memdb.NewTestDB(t)

	rc := NewQMDBRootComputer()
	rc.EnableUndoRecording()

	mkBlock := func(base, n int, nonce uint64) map[types.Address]*account.StateAccount {
		accts := make(map[types.Address]*account.StateAccount, n)
		for i := 0; i < n; i++ {
			accts[rlAddr(base+i)] = qmAcct(nonce, uint64(1000+base+i))
		}
		return accts
	}

	// Three committed blocks, wide enough to cross twig boundaries.
	var undo3 *qmdb.BlockUndo
	var root3 types.Hash
	rlApplyBlock(t, db, rc, mkBlock(0, 1200, 1))
	rlApplyBlock(t, db, rc, mkBlock(600, 1200, 2)) // overwrites 600..1199 → kills via index
	undo3, root3 = rlApplyBlock(t, db, rc, mkBlock(300, 1200, 3))

	// A revert whose tx rolls back: memory (tree + index) diverges from disk.
	rwtx, err := db.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.RevertBlock(rwtx, undo3); err != nil {
		t.Fatalf("RevertBlock: %v", err)
	}
	rwtx.Rollback()

	// Recovery reload — the exact unwindForReimport failure path.
	if err := db.Update(t.Context(), func(tx kv.RwTx) error {
		rc.SetCold(tx)
		defer rc.SetCold(nil)
		return rc.LoadFrom(tx)
	}); err != nil {
		t.Fatalf("recovery LoadFrom: %v", err)
	}
	if got := rc.Root(); got != root3 {
		t.Fatalf("recovered tree root=%x want block-3 root %x", got[:8], root3[:8])
	}

	// Control computer: a clean reload of the same store (what every other
	// node in the cluster effectively is).
	ctl := NewQMDBRootComputer()
	ctl.EnableUndoRecording()
	if err := db.Update(t.Context(), func(tx kv.RwTx) error {
		ctl.SetCold(tx)
		defer ctl.SetCold(nil)
		return ctl.LoadFrom(tx)
	}); err != nil {
		t.Fatalf("control LoadFrom: %v", err)
	}
	if ctl.Root() != root3 {
		t.Fatalf("control root=%x want %x", ctl.Root().Bytes()[:8], root3[:8])
	}

	// Post-recovery block overwrites keys the rolled-back revert had touched:
	// with a stale index the recovered computer deactivates the wrong slots.
	// The control computes in-memory on a RoTx pinned BEFORE the recovered
	// computer flushes (its dead-row reclamation would delete rows the
	// control's kills still read).
	post := mkBlock(300, 1200, 9)
	ctlTx, err := db.BeginRo(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer ctlTx.Rollback()
	ctl.SetCold(ctlTx)
	rootB, err := ctl.ComputeRoot(post, nil)
	ctl.SetCold(nil)
	if err != nil {
		t.Fatalf("control ComputeRoot: %v", err)
	}
	_, rootA := rlApplyBlock(t, db, rc, post)
	if rootA != rootB {
		t.Fatalf("post-recovery roots diverged: recovered=%x control=%x — stale in-RAM index survived the reload", rootA[:8], rootB[:8])
	}
}
