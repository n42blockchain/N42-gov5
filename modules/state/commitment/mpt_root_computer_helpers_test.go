// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers the standalone MPT persistence helpers and small store types left at
// 0%: WriteMPTRoot/ReadMPTRoot, WriteMPTTrieState/ReadMPTTrieState,
// WriteBulkCheckpoint/ReadBulkCheckpoint/DeleteBulkCheckpoint round trips;
// memBranchStore.Reset/Len; mdbxBranchStore.Get/SetReadTx (including the
// nil-roTx "not wired yet" case); and PlainStateMPTReader.ReadAllStorage.

package commitment

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// mptTestTx opens a memdb tx with the N42 table set (MPTRoot/branch tables are
// not in lib/kv's default configuration).
func mptTestTx(t *testing.T) (kv.RwDB, kv.RwTx) {
	t.Helper()
	prevCfg := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevCfg })
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tx.Rollback)
	return db, tx
}

func TestMPTRootReadWrite(t *testing.T) {
	_, tx := mptTestTx(t)

	if got, err := ReadMPTRoot(tx); err != nil || got != (types.Hash{}) {
		t.Fatalf("ReadMPTRoot on empty = (%x,%v), want (zero,nil)", got, err)
	}

	want := types.Hash{1: 0xaa, 31: 0xbb}
	if err := WriteMPTRoot(tx, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMPTRoot(tx)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("ReadMPTRoot = %x, want %x", got, want)
	}
}

func TestMPTTrieStateReadWrite(t *testing.T) {
	_, tx := mptTestTx(t)

	if bn, st, err := ReadMPTTrieState(tx); err != nil || bn != 0 || st != nil {
		t.Fatalf("ReadMPTTrieState on empty = (%d,%v,%v), want (0,nil,nil)", bn, st, err)
	}

	state := []byte("some-serialized-hph-state")
	if err := WriteMPTTrieState(tx, 42, state); err != nil {
		t.Fatal(err)
	}
	bn, st, err := ReadMPTTrieState(tx)
	if err != nil {
		t.Fatal(err)
	}
	if bn != 42 || string(st) != string(state) {
		t.Fatalf("ReadMPTTrieState = (%d,%q), want (42,%q)", bn, st, state)
	}
}

func TestBulkCheckpointReadWriteDelete(t *testing.T) {
	_, tx := mptTestTx(t)

	if lastKey, st, err := ReadBulkCheckpoint(tx); err != nil || lastKey != nil || st != nil {
		t.Fatalf("ReadBulkCheckpoint on empty = (%v,%v,%v), want (nil,nil,nil)", lastKey, st, err)
	}

	lastAccKey := []byte{1, 2, 3, 4, 5}
	trieState := []byte("checkpoint-trie-state")
	if err := WriteBulkCheckpoint(tx, lastAccKey, trieState); err != nil {
		t.Fatal(err)
	}
	gotKey, gotState, err := ReadBulkCheckpoint(tx)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotKey) != string(lastAccKey) || string(gotState) != string(trieState) {
		t.Fatalf("ReadBulkCheckpoint = (%x,%q), want (%x,%q)", gotKey, gotState, lastAccKey, trieState)
	}

	if err := DeleteBulkCheckpoint(tx); err != nil {
		t.Fatal(err)
	}
	if lastKey, st, err := ReadBulkCheckpoint(tx); err != nil || lastKey != nil || st != nil {
		t.Fatalf("ReadBulkCheckpoint after delete = (%v,%v,%v), want (nil,nil,nil)", lastKey, st, err)
	}
}

func TestMemBranchStoreResetLen(t *testing.T) {
	s := newMemBranchStore()
	if s.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", s.Len())
	}
	s.Put([]byte("k1"), []byte("v1"))
	s.Put([]byte("k2"), []byte("v2"))
	if s.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", s.Len())
	}
	s.Reset()
	if s.Len() != 0 {
		t.Fatalf("Len() after Reset = %d, want 0", s.Len())
	}
	if v, err := s.Get([]byte("k1")); err != nil || v != nil {
		t.Fatalf("Get after Reset = (%v,%v), want (nil,nil)", v, err)
	}
}

func TestMDBXBranchStoreGetSetReadTx(t *testing.T) {
	_, tx := mptTestTx(t)
	const table = modules.MPTBranch
	if err := tx.Put(table, []byte("p1"), []byte("node-data")); err != nil {
		t.Fatal(err)
	}

	s := newMDBXBranchStore(table)
	// Before SetReadTx, Get must report "not found" rather than panicking.
	if v, err := s.Get([]byte("p1")); err != nil || v != nil {
		t.Fatalf("Get before SetReadTx = (%q,%v), want (nil,nil)", v, err)
	}

	s.SetReadTx(tx)
	v, err := s.Get([]byte("p1"))
	if err != nil || string(v) != "node-data" {
		t.Fatalf("Get after SetReadTx = (%q,%v), want (node-data,nil)", v, err)
	}
	if v, err := s.Get([]byte("missing")); err != nil || v != nil {
		t.Fatalf("Get(missing) = (%q,%v), want (nil,nil)", v, err)
	}
}

func TestPlainStateMPTReaderReadAllStorage(t *testing.T) {
	_, tx := mptTestTx(t)
	addr := types.Address{19: 7}
	other := types.Address{19: 8}

	put := func(a types.Address, slot types.Hash, val uint64) {
		key := modules.PlainGenerateCompositeStorageKey(a[:], slot[:])
		v := new(uint256.Int).SetUint64(val)
		b := v.Bytes32()
		if err := tx.Put(modules.Storage, key, b[:]); err != nil {
			t.Fatal(err)
		}
	}
	s1, s2 := types.Hash{31: 1}, types.Hash{31: 2}
	put(addr, s1, 111)
	put(addr, s2, 222)
	put(other, s1, 999)

	r := NewPlainStateMPTReader(tx)
	got, err := r.ReadAllStorage(addr)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ReadAllStorage returned %d slots, want 2", len(got))
	}
	if v, ok := got[s1]; !ok || v.Uint64() != 111 {
		t.Fatalf("slot s1 = %v, want 111", v)
	}
	if v, ok := got[s2]; !ok || v.Uint64() != 222 {
		t.Fatalf("slot s2 = %v, want 222", v)
	}

	// Clone must read through the given tx independently.
	cloned := r.Clone(tx)
	got2, err := cloned.ReadAllStorage(addr)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 2 {
		t.Fatalf("cloned ReadAllStorage returned %d slots, want 2", len(got2))
	}
}
