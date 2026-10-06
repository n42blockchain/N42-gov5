// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// CacheStats/TierStats/LRUStats start at zero and report populated budgets
// for a freshly built buffer.
func TestPlainStateBufferStatsInitial(t *testing.T) {
	buf := NewPlainStateBuffer()

	hits, misses := buf.CacheStats()
	if hits != 0 || misses != 0 {
		t.Fatalf("CacheStats = %d, %d, want 0, 0", hits, misses)
	}

	ah, am, sh, sm, ch, cm := buf.TierStats()
	if ah != 0 || am != 0 || sh != 0 || sm != 0 || ch != 0 || cm != 0 {
		t.Fatalf("TierStats = %d %d %d %d %d %d, want all zero", ah, am, sh, sm, ch, cm)
	}

	accB, stoB, codeB, accE, stoE, codeE := buf.LRUStats()
	if accB != 0 || stoB != 0 || codeB != 0 || accE != 0 || stoE != 0 || codeE != 0 {
		t.Fatalf("LRUStats on empty buffer = %v", []int64{accB, stoB, codeB, int64(accE), int64(stoE), int64(codeE)})
	}
}

// CacheAccount/CacheStorage/CacheCode populate the read caches, and the
// If-Absent variants only populate when no entry already exists.
func TestPlainStateBufferCachePrimitives(t *testing.T) {
	buf := NewPlainStateBuffer()
	addr := types.HexToAddress("0x1000000000000000000000000000000000000001")
	slot := types.HexToHash("0x01")
	codeHash := types.HexToHash("0x02")

	// Negative cache: nil account.
	buf.CacheAccount(addr, nil)
	if _, ok := buf.LookupReadAccount(addr); !ok {
		t.Fatal("LookupReadAccount: expected negative cache hit")
	}

	acc := &account.StateAccount{Nonce: 3}
	buf.CacheAccount(addr, acc)
	v, ok := buf.LookupReadAccount(addr)
	if !ok || len(v) == 0 {
		t.Fatalf("LookupReadAccount after positive cache = %v, %v", v, ok)
	}

	buf.CacheStorage(addr, slot, []byte{0x42})
	got, ok := buf.LookupReadStorage(addr, slot)
	if !ok || len(got) != 1 || got[0] != 0x42 {
		t.Fatalf("LookupReadStorage = %v, %v", got, ok)
	}

	buf.CacheCode(codeHash, []byte("bytecode"))
	code, ok := buf.LookupReadCode(codeHash)
	if !ok || string(code) != "bytecode" {
		t.Fatalf("LookupReadCode = %q, %v", code, ok)
	}

	// IfAbsent: first call populates, second is a no-op (false).
	slot2 := types.HexToHash("0x03")
	if !buf.CacheStorageIfAbsent(addr, slot2, []byte{0x07}) {
		t.Fatal("CacheStorageIfAbsent: expected true on first insert")
	}
	if buf.CacheStorageIfAbsent(addr, slot2, []byte{0x08}) {
		t.Fatal("CacheStorageIfAbsent: expected false when already present")
	}
	got2, _ := buf.LookupReadStorage(addr, slot2)
	if len(got2) != 1 || got2[0] != 0x07 {
		t.Fatalf("CacheStorageIfAbsent overwrote existing entry: %v", got2)
	}

	codeHash2 := types.HexToHash("0x04")
	if !buf.CacheCodeIfAbsent(codeHash2, []byte("first")) {
		t.Fatal("CacheCodeIfAbsent: expected true on first insert")
	}
	if buf.CacheCodeIfAbsent(codeHash2, []byte("second")) {
		t.Fatal("CacheCodeIfAbsent: expected false when already present")
	}
	got3, _ := buf.LookupReadCode(codeHash2)
	if string(got3) != "first" {
		t.Fatalf("CacheCodeIfAbsent overwrote existing entry: %q", got3)
	}
}

// InFlightSnapshot/ClearInFlight expose whatever SnapshotForFlush installed.
func TestPlainStateBufferInFlightSnapshot(t *testing.T) {
	buf := NewPlainStateBuffer()
	if got := buf.InFlightSnapshot(); got != nil {
		t.Fatalf("InFlightSnapshot (fresh buffer) = %v, want nil", got)
	}

	addr := types.HexToAddress("0x1000000000000000000000000000000000000005")
	buf.accounts[addr] = []byte("enc")

	snap := buf.SnapshotForFlush()
	if snap == nil {
		t.Fatal("SnapshotForFlush returned nil")
	}
	if got := buf.InFlightSnapshot(); got != snap {
		t.Fatalf("InFlightSnapshot = %v, want %v", got, snap)
	}

	enc, ok := snap.LookupAccount(addr)
	if !ok || string(enc) != "enc" {
		t.Fatalf("LookupAccount = %q, %v", enc, ok)
	}
	if _, ok := snap.LookupAccount(types.HexToAddress("0xdead")); ok {
		t.Fatal("LookupAccount: unexpected hit for untouched address")
	}

	buf.ClearInFlight()
	if got := buf.InFlightSnapshot(); got != nil {
		t.Fatalf("InFlightSnapshot after ClearInFlight = %v, want nil", got)
	}
}

// LookupStorage distinguishes an explicit slot, a wiped address, and an
// address with no record at all.
func TestBufferSnapshotLookupStorage(t *testing.T) {
	var nilSnap *BufferSnapshot
	if v, ok := nilSnap.LookupStorage(types.Address{}, types.Hash{}); v != nil || ok {
		t.Fatalf("nil snapshot LookupStorage = %v, %v", v, ok)
	}
	if v, ok := nilSnap.LookupAccount(types.Address{}); v != nil || ok {
		t.Fatalf("nil snapshot LookupAccount = %v, %v", v, ok)
	}

	buf := NewPlainStateBuffer()
	addr1 := types.HexToAddress("0x01")
	addr2 := types.HexToAddress("0x02")
	slot := types.HexToHash("0x01")

	buf.storage[addr1] = map[types.Hash]storageEntry{}
	buf.storage[addr1][slot] = storageEntry{valLen: 1, value: [32]byte{31: 0x09}}
	buf.wipedStorage[addr2] = struct{}{}

	snap := buf.SnapshotForFlush()

	v, ok := snap.LookupStorage(addr1, slot)
	if !ok || len(v) != 1 || v[0] != 0x09 {
		t.Fatalf("LookupStorage explicit slot = %v, %v", v, ok)
	}
	v, ok = snap.LookupStorage(addr2, slot)
	if !ok || v != nil {
		t.Fatalf("LookupStorage wiped address = %v, %v, want nil,true", v, ok)
	}
	v, ok = snap.LookupStorage(types.HexToAddress("0x03"), slot)
	if ok || v != nil {
		t.Fatalf("LookupStorage unknown address = %v, %v, want nil,false", v, ok)
	}
}

// PruneWipesBefore drops stamped wipe records at or below the cutoff, and a
// zero cutoff is a no-op.
func TestPruneWipesBefore(t *testing.T) {
	buf := NewPlainStateBuffer()
	addr := types.HexToAddress("0x09")

	epoch := buf.StampWipes([]types.Address{addr})
	if !buf.IsWipedAfter(addr, 0) {
		t.Fatal("IsWipedAfter: expected true for epoch 0 baseline")
	}

	buf.PruneWipesBefore(0) // no-op
	if !buf.IsWipedAfter(addr, 0) {
		t.Fatal("PruneWipesBefore(0) unexpectedly pruned a record")
	}

	buf.PruneWipesBefore(epoch)
	if buf.IsWipedAfter(addr, 0) {
		t.Fatal("PruneWipesBefore did not drop the stamped record")
	}
}

// SetTrackedAddr / isTracked and FlushDeletesLog are safe no-ops to exercise
// directly (the log file write path only activates lazily).
func TestTrackedAddrAndFlushDeletesLog(t *testing.T) {
	addr := types.HexToAddress("0x0a")
	SetTrackedAddr(&addr)
	if !isTracked(addr) {
		t.Fatal("isTracked: expected true for tracked address")
	}
	other := types.HexToAddress("0x0b")
	if isTracked(other) {
		t.Fatal("isTracked: expected false for untracked address")
	}
	SetTrackedAddr(nil)
	if isTracked(addr) {
		t.Fatal("isTracked: expected false after clearing tracked address")
	}

	// Must not panic even though no delete was ever recorded.
	FlushDeletesLog()
}
