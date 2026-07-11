// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for the three-state (tree/PlainState/marker) discontinuity realign.
// The QMDB tree itself is not exercised here — realignAppliedToTree treats it
// as the already-correct reference and only rolls PlainState + bookkeeping +
// marker back to it; that contract is what these tests pin down.

package internal

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
)

// newRealignTestDB opens a memdb carrying the full N42 table set (the realign
// touches N42-specific tables like QMDBUndoWindow that the default chaindata
// cfg lacks).
func newRealignTestDB(t *testing.T) kv.RwDB {
	t.Helper()
	modules.N42Init()
	previousCfg := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() {
		kv.ChaindataTablesCfg = previousCfg
	})
	return memdb.NewTestDB(t)
}

// seedTwoBlocksAheadOfTree writes the disk shape of a discontinuity: the tree
// (not modeled) sits at block 10 while PlainState absorbed blocks 11 and 12 —
// changesets, undo records, evidence and receipts for both are present.
// Returns the addresses involved and the pre-block-11 account for asserts.
func seedTwoBlocksAheadOfTree(t *testing.T, tx kv.RwTx) (existing, fresh types.Address, pre *account.StateAccount) {
	t.Helper()
	emptyCH := crypto.Keccak256Hash(nil)
	existing[19], fresh[19] = 0xE1, 0xF1

	// State as of block 10: only `existing`, nonce 5, slot 0x..01 = 7.
	pre = &account.StateAccount{Initialised: true, Nonce: 5, CodeHash: emptyCH}
	pre.Balance.SetUint64(1000)
	if err := tx.Put(modules.Account, existing[:], pre.MarshalV2()); err != nil {
		t.Fatal(err)
	}
	slotKey := modules.PlainGenerateCompositeStorageKey(existing.Bytes(), types.Hash{31: 1}.Bytes())
	if err := tx.Put(modules.Storage, slotKey, []byte{7}); err != nil {
		t.Fatal(err)
	}

	// Block 11: existing 5→6, fresh created with nonce 3, slot 7→8.
	w11 := state.NewPlainStateWriter(tx, tx, 11)
	e11 := &account.StateAccount{Initialised: true, Nonce: 6, CodeHash: emptyCH}
	e11.Balance.SetUint64(900)
	if err := w11.UpdateAccountData(existing, pre, e11); err != nil {
		t.Fatal(err)
	}
	f11 := &account.StateAccount{Initialised: true, Nonce: 3, CodeHash: emptyCH}
	f11.Balance.SetUint64(50)
	if err := w11.UpdateAccountData(fresh, &account.StateAccount{}, f11); err != nil {
		t.Fatal(err)
	}
	if err := w11.WriteAccountStorage(existing, types.Hash{31: 1}, *uint256.NewInt(7), *uint256.NewInt(8)); err != nil {
		t.Fatal(err)
	}
	if err := w11.WriteChangeSets(); err != nil {
		t.Fatal(err)
	}

	// Block 12: existing 6→7, slot 8→9.
	w12 := state.NewPlainStateWriter(tx, tx, 12)
	e12 := &account.StateAccount{Initialised: true, Nonce: 7, CodeHash: emptyCH}
	e12.Balance.SetUint64(800)
	if err := w12.UpdateAccountData(existing, e11, e12); err != nil {
		t.Fatal(err)
	}
	if err := w12.WriteAccountStorage(existing, types.Hash{31: 1}, *uint256.NewInt(8), *uint256.NewInt(9)); err != nil {
		t.Fatal(err)
	}
	if err := w12.WriteChangeSets(); err != nil {
		t.Fatal(err)
	}

	// Per-block bookkeeping the realign must consume.
	for _, n := range []uint64{11, 12} {
		key := modules.EncodeBlockNumber(n)
		if err := tx.Put(modules.QMDBUndoWindow, key, []byte{0xAA}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Put(modules.ConsensusEvidence, key, []byte{0xBB}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Put(modules.Receipts, key, []byte{0xCC}); err != nil {
			t.Fatal(err)
		}
	}
	return existing, fresh, pre
}

// assertRealignedToBlock10 checks the full post-realign disk shape.
func assertRealignedToBlock10(t *testing.T, tx kv.RwTx, existing, fresh types.Address, pre *account.StateAccount, wantHash types.Hash) {
	t.Helper()
	// PlainState back at block 10.
	av, err := tx.GetOne(modules.Account, existing[:])
	if err != nil {
		t.Fatal(err)
	}
	got := new(account.StateAccount)
	if err := got.DecodeForStorageV2(av); err != nil {
		t.Fatal(err)
	}
	if got.Nonce != pre.Nonce || got.Balance.Cmp(&pre.Balance) != 0 {
		t.Fatalf("existing account not restored: nonce=%d balance=%s, want nonce=%d balance=%s",
			got.Nonce, got.Balance.String(), pre.Nonce, pre.Balance.String())
	}
	if fv, _ := tx.GetOne(modules.Account, fresh[:]); len(fv) != 0 {
		t.Fatalf("account created above the realign target survived: %x", fv)
	}
	slotKey := modules.PlainGenerateCompositeStorageKey(existing.Bytes(), types.Hash{31: 1}.Bytes())
	sv, _ := tx.GetOne(modules.Storage, slotKey)
	if len(sv) != 1 || sv[0] != 7 {
		t.Fatalf("slot not restored to pre-block-11 value 7: %x", sv)
	}
	// Bookkeeping consumed.
	for _, n := range []uint64{11, 12} {
		key := modules.EncodeBlockNumber(n)
		for _, table := range []string{modules.QMDBUndoWindow, modules.ConsensusEvidence, modules.Receipts,
			modules.AccountChangeSet} {
			if v, _ := tx.GetOne(table, key); len(v) != 0 {
				t.Fatalf("%s row for block %d survived the realign", table, n)
			}
		}
	}
	if last, err := highestChangesetBlock(tx); err != nil || last > 10 {
		t.Fatalf("changeset head after realign = %d (err %v), want <= 10", last, err)
	}
	// Marker moved to the target.
	num, hash, ok, err := rawdb.ReadQMDBApplied(tx)
	if err != nil || !ok {
		t.Fatalf("applied marker unreadable: ok=%v err=%v", ok, err)
	}
	if num != 10 || hash != wantHash {
		t.Fatalf("marker at %d/%x, want 10/%x", num, hash[:8], wantHash[:8])
	}
}

// TestRealignAppliedToTree_ThreeStateRollback is the ensureQMDBTreeAtParent
// path: the tree rewound to 10, the marker (and PlainState) still claim 12.
func TestRealignAppliedToTree_ThreeStateRollback(t *testing.T) {
	db := newRealignTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	existing, fresh, pre := seedTwoBlocksAheadOfTree(t, tx)
	if err := rawdb.WriteQMDBApplied(tx, 12, types.Hash{0x12}); err != nil {
		t.Fatal(err)
	}

	target := types.Hash{0x10}
	if err := realignAppliedToTree(tx, 10, target); err != nil {
		t.Fatalf("realign: %v", err)
	}
	assertRealignedToBlock10(t, tx, existing, fresh, pre, target)
}

// TestRealignAppliedToTree_HalfHealedMarker is the live node4 shape: an old
// marker-only realign already moved the marker to 10, but PlainState (and its
// changesets) still hold 11-12. The changeset head — not the marker — must
// drive the rollback.
func TestRealignAppliedToTree_HalfHealedMarker(t *testing.T) {
	db := newRealignTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	existing, fresh, pre := seedTwoBlocksAheadOfTree(t, tx)
	target := types.Hash{0x10}
	if err := rawdb.WriteQMDBApplied(tx, 10, target); err != nil { // marker already realigned
		t.Fatal(err)
	}

	if last, _ := highestChangesetBlock(tx); last != 12 {
		t.Fatalf("detector: changeset head = %d, want 12", last)
	}
	if err := realignAppliedToTree(tx, 10, target); err != nil {
		t.Fatalf("realign: %v", err)
	}
	assertRealignedToBlock10(t, tx, existing, fresh, pre, target)
}

// TestRealignAppliedToTree_NothingAhead: PlainState at the target already —
// the realign degrades to a pure marker write.
func TestRealignAppliedToTree_NothingAhead(t *testing.T) {
	db := newRealignTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	target := types.Hash{0x10}
	if err := realignAppliedToTree(tx, 10, target); err != nil {
		t.Fatalf("realign on an empty store: %v", err)
	}
	num, hash, ok, err := rawdb.ReadQMDBApplied(tx)
	if err != nil || !ok || num != 10 || hash != target {
		t.Fatalf("marker = %d/%x ok=%v err=%v, want 10/%x", num, hash[:8], ok, err, target[:8])
	}
}

// TestRealignAppliedToTree_RefusesBeyondWindow: a span wider than the
// changeset window must be rejected — a partial heal is worse than wedging.
func TestRealignAppliedToTree_RefusesBeyondWindow(t *testing.T) {
	db := newRealignTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	w := state.NewPlainStateWriter(tx, tx, 10_000)
	emptyCH := crypto.Keccak256Hash(nil)
	var addr types.Address
	addr[19] = 0xD1
	a := &account.StateAccount{Initialised: true, Nonce: 1, CodeHash: emptyCH}
	if err := w.UpdateAccountData(addr, &account.StateAccount{}, a); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteChangeSets(); err != nil {
		t.Fatal(err)
	}

	if err := realignAppliedToTree(tx, 100, types.Hash{0x01}); err == nil {
		t.Fatal("realign across 9900 blocks succeeded, want refusal beyond the changeset window")
	}
}

// TestHighestChangesetBlock_TakesMaxOfBothTables: storage-only blocks (no
// account delta) must still be seen by the detector.
func TestHighestChangesetBlock_TakesMaxOfBothTables(t *testing.T) {
	db := newRealignTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	if last, err := highestChangesetBlock(tx); err != nil || last != 0 {
		t.Fatalf("empty store: got %d err=%v, want 0", last, err)
	}

	emptyCH := crypto.Keccak256Hash(nil)
	var addr types.Address
	addr[19] = 0xA1
	w20 := state.NewPlainStateWriter(tx, tx, 20)
	a := &account.StateAccount{Initialised: true, Nonce: 1, CodeHash: emptyCH}
	if err := w20.UpdateAccountData(addr, &account.StateAccount{}, a); err != nil {
		t.Fatal(err)
	}
	if err := w20.WriteChangeSets(); err != nil {
		t.Fatal(err)
	}
	w25 := state.NewPlainStateWriter(tx, tx, 25) // storage-only block
	if err := w25.WriteAccountStorage(addr, types.Hash{31: 2}, *uint256.NewInt(0), *uint256.NewInt(1)); err != nil {
		t.Fatal(err)
	}
	if err := w25.WriteChangeSets(); err != nil {
		t.Fatal(err)
	}

	if last, err := highestChangesetBlock(tx); err != nil || last != 25 {
		t.Fatalf("got %d err=%v, want 25 (storage changeset)", last, err)
	}
}
