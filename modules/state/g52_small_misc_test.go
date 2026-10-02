// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"testing"

	"github.com/google/btree"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// TestG52StorageItemLess pins the btree.Item ordering storageItem provides
// for PlainState's per-contract slot cache: ordering is by raw key bytes,
// not by seckey or value.
func TestG52StorageItemLess(t *testing.T) {
	a := &storageItem{key: types.HexToHash("0x01")}
	b := &storageItem{key: types.HexToHash("0x02")}
	if !a.Less(b) {
		t.Fatal("0x01 should sort before 0x02")
	}
	if b.Less(a) == false && a.Less(b) == false {
		t.Fatal("exactly one of a.Less(b)/b.Less(a) must hold for distinct keys")
	}
	if a.Less(a) {
		t.Fatal("a key must not be Less than itself")
	}
	var bt btree.Item = b
	if !a.Less(bt) {
		t.Fatal("Less must accept any btree.Item, not just *storageItem")
	}
}

// TestG52PlainStateSetCodeSource pins the trivial setter that wires a
// bytecode fast path into PlainState's historical reads.
func TestG52PlainStateSetCodeSource(t *testing.T) {
	ps := &PlainState{}
	stub := &g52CodeSourceStub{}
	ps.SetCodeSource(stub)
	if ps.codeSrc != stub {
		t.Fatal("SetCodeSource should store the given CodeSource")
	}
}

// TestG52HashedCanonicalWriterCodeAndTrace covers NewHashedCanonicalWriter's
// constructor, UpdateAccountCode's write-and-skip-empty behavior, and the
// thin SetTrace setter on HashedStateReader.
func TestG52HashedCanonicalWriterCodeAndTrace(t *testing.T) {
	db := mdbxTestDB(t)
	ctx := context.Background()

	codeHash := types.HexToHash("0x0b")
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		w := NewHashedCanonicalWriter(tx, 1)
		if err := w.UpdateAccountCode(types.Address{}, codeHash, nil); err != nil {
			t.Fatalf("UpdateAccountCode(empty code) should no-op: %v", err)
		}
		if err := w.UpdateAccountCode(types.Address{}, codeHash, []byte{0x60, 0x01}); err != nil {
			t.Fatalf("UpdateAccountCode: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(ctx, func(tx kv.Tx) error {
		got, err := tx.GetOne(modules.Code, codeHash[:])
		if err != nil || string(got) != "\x60\x01" {
			t.Fatalf("UpdateAccountCode did not persist: got=%x err=%v", got, err)
		}
		r := NewHashedStateReader(tx)
		r.SetTrace(true)
		if !r.trace {
			t.Fatal("SetTrace(true) should set the trace flag")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestG52PlainStateWriterChangedCountsAndHistoryAggregator pins the thin
// forwarding methods PlainStateWriter exposes over its embedded
// ChangeSetWriter: a no-history writer reports (0,0) and tolerates a nil
// SetHistoryAggregator call; a history-backed writer reports real counts
// and forwards the aggregator.
func TestG52PlainStateWriterChangedCountsAndHistoryAggregator(t *testing.T) {
	noHist := NewPlainStateWriterNoHistory(nil)
	if accounts, storage := noHist.ChangedCounts(); accounts != 0 || storage != 0 {
		t.Fatalf("no-history writer ChangedCounts = (%d,%d), want (0,0)", accounts, storage)
	}
	noHist.SetHistoryAggregator(nil) // must not panic without a csw

	db := mdbxTestDB(t)
	ctx := context.Background()
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		w := NewPlainStateWriter(tx, tx, 1)
		addr := types.HexToAddress("0x00000000000000000000000000000000000000e9")
		if err := w.WriteAccountStorage(addr, types.HexToHash("0x01"), uint256.Int{}, *uint256.NewInt(1)); err != nil {
			return err
		}
		accounts, storage := w.ChangedCounts()
		if accounts != 0 || storage != 1 {
			t.Fatalf("ChangedCounts after one storage write = (%d,%d), want (0,1)", accounts, storage)
		}
		w.SetHistoryAggregator(nil) // forwards to csw; nil agg is a valid "disable" value
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestG52CapturingTxDelete covers capturingPutDel.Delete: deleting a warm
// key must both record its pre-image in the diff and actually remove the
// row, so a Restore brings it back.
func TestG52CapturingTxDelete(t *testing.T) {
	db := mdbxTestDB(t)
	ctx := context.Background()
	key := []byte("deleteme")

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Put(modules.Account, key, []byte{0x42})
	}); err != nil {
		t.Fatal(err)
	}

	diff := NewBlockRevDiff()
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		cTx := NewCapturingTx(tx, diff)
		return cTx.Delete(modules.Account, key)
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne(modules.Account, key)
		if err != nil || v != nil {
			t.Fatalf("key should be gone after Delete: v=%x err=%v", v, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return diff.Restore(tx)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.View(ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne(modules.Account, key)
		if err != nil || string(v) != "\x42" {
			t.Fatalf("Restore should bring the deleted key back: v=%x err=%v", v, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestG52SnapshotReadAccountStorage covers the readable-Snapshot
// ReadAccountStorage path: an indexed item, a miss with no getOneFun (nil,
// nil), and a miss that falls through to getOneFun.
func TestG52SnapshotReadAccountStorage(t *testing.T) {
	snap := NewReadableSnapshot()
	addr := types.HexToAddress("0x00000000000000000000000000000000000000ea")
	key := types.HexToHash("0x0c")
	compositeKey := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), key.Bytes())
	snap.Items = append(snap.Items, &Item{Key: compositeKey, Value: []byte{0x5a}})
	snap.storage[string(compositeKey)] = 0

	got, err := snap.ReadAccountStorage(addr, &key)
	if err != nil || len(got) != 1 || got[0] != 0x5a {
		t.Fatalf("indexed ReadAccountStorage = %x, %v; want [0x5a]", got, err)
	}

	other := types.HexToHash("0x0d")
	if got, err := snap.ReadAccountStorage(addr, &other); err != nil || got != nil {
		t.Fatalf("miss with no getOneFun should be (nil,nil): got=%x err=%v", got, err)
	}

	called := false
	snap.SetGetFun(func(table string, k []byte) ([]byte, error) {
		called = true
		if table != modules.Storage {
			t.Fatalf("getOneFun table = %q, want %q", table, modules.Storage)
		}
		return []byte{0x99}, nil
	})
	got2, err := snap.ReadAccountStorage(addr, &other)
	if err != nil || !called || len(got2) != 1 || got2[0] != 0x99 {
		t.Fatalf("fallback ReadAccountStorage = %x, %v, called=%v; want [0x99], true", got2, err, called)
	}
}

// mdbxTestDB opens a throwaway in-memory MDBX database with the real
// N42 table config, for tests that need DupSort / AutoConv semantics
// plain memdb.NewTestDB does not configure.
func mdbxTestDB(t *testing.T) kv.RwDB {
	t.Helper()
	db := mdbx.NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(journalTestCfg).MustOpen()
	t.Cleanup(db.Close)
	return db
}
