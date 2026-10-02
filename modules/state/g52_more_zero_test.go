// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// TestG52ErrAbortRetryError pins errAbortRetry's Error() string, the one
// method on the abort-retry sentinel nothing else in the suite calls
// directly (callers only ever go through IsAbortRetry/AbortRetry).
func TestG52ErrAbortRetryError(t *testing.T) {
	err := AbortRetry()
	if err.Error() != "parallel EVM: abort and retry" {
		t.Fatalf("errAbortRetry.Error() = %q, want the documented sentinel message", err.Error())
	}
	if !IsAbortRetry(err) {
		t.Fatal("AbortRetry()'s own error should satisfy IsAbortRetry")
	}
}

// TestG52HistoryAggregatorAddKey covers the direct (key, block) recording
// path used by the backfiller, which reads changeset rows back from the
// database rather than holding a ChangeSet in memory.
func TestG52HistoryAggregatorAddKey(t *testing.T) {
	agg := NewHistoryAggregator()
	key := []byte("addrkey")

	agg.AddKey(modules.AccountsHistory, key, 10)
	agg.AddKey(modules.AccountsHistory, key, 20)
	bm, ok := agg.accounts[string(key)]
	if !ok || bm.GetCardinality() != 2 || !bm.Contains(10) || !bm.Contains(20) {
		t.Fatalf("AddKey(AccountsHistory) did not record both blocks: ok=%v bm=%v", ok, bm)
	}

	stoKey := []byte("stokey")
	agg.AddKey(modules.StorageHistory, stoKey, 30)
	bm2, ok := agg.storage[string(stoKey)]
	if !ok || !bm2.Contains(30) {
		t.Fatalf("AddKey(StorageHistory) did not route into the storage map: ok=%v bm=%v", ok, bm2)
	}
}

// TestG52MapApplyTargetPutCode covers the idempotent code-write path: a
// first PutCode records the bytes, and a second call for the SAME hash
// (even with different bytes, which must never happen under correct
// content-addressing, but the no-op guard must still hold) is a no-op.
func TestG52MapApplyTargetPutCode(t *testing.T) {
	m := NewMapApplyTarget()
	codeHash := types.HexToHash("0x0e")

	if err := m.PutCode(codeHash, []byte{0x60, 0x01}); err != nil {
		t.Fatalf("PutCode: %v", err)
	}
	if got := m.Code[codeHash]; string(got) != "\x60\x01" {
		t.Fatalf("PutCode did not record the bytes: got=%x", got)
	}
	if err := m.PutCode(codeHash, []byte{0xff}); err != nil {
		t.Fatalf("second PutCode: %v", err)
	}
	if got := m.Code[codeHash]; string(got) != "\x60\x01" {
		t.Fatalf("second PutCode for an existing hash must be a no-op, got=%x", got)
	}
}

// TestG52PlainStateForEachStorage drives PlainState.ForEachStorage over one
// account with two slots (both present at the tip, no history changes),
// exercising the WalkAsOfStorage-fed btree build and the final
// AscendGreaterOrEqual callback dispatch -- the only path that reaches this
// function in the whole suite.
func TestG52PlainStateForEachStorage(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevTables })

	addr := types.HexToAddress("0x1000000000000000000000000000000000000044")
	slotA := types.HexToHash("0x01")
	slotB := types.HexToHash("0x02")

	db := memdb.NewTestDB(t)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		w := NewPlainStateWriter(tx, tx, 1)
		if err := w.WriteAccountStorage(addr, slotA, uint256.Int{}, *uint256.NewInt(11)); err != nil {
			return err
		}
		if err := w.WriteAccountStorage(addr, slotB, uint256.Int{}, *uint256.NewInt(22)); err != nil {
			return err
		}
		if err := w.csw.WriteChangeSets(); err != nil {
			return err
		}
		return w.csw.WriteHistory()
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		ps := NewPlainState(tx, 2)
		found := map[types.Hash]uint64{}
		err := ps.ForEachStorage(addr, types.Hash{}, func(key, seckey types.Hash, value uint256.Int) bool {
			found[key] = value.Uint64()
			return true
		}, 10)
		if err != nil {
			t.Fatalf("ForEachStorage: %v", err)
		}
		if found[slotA] != 11 || found[slotB] != 22 {
			t.Fatalf("ForEachStorage results = %v, want {slotA:11, slotB:22}", found)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
