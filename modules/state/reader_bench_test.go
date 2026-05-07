// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// BenchmarkReadAccountStorage_BufHit measures the active-buffer hit
// path — the most common case in practice (the EVM repeatedly SLOAD's
// slots that some earlier tx in the same block SSTORE'd).
//
// Pre-fix this path was already alloc-free; the benchmark exists as a
// regression baseline so the LRU+MDBX optimisations don't accidentally
// pessimise it.
func BenchmarkReadAccountStorage_BufHit(b *testing.B) {
	addr := types.HexToAddress("0x00000000000000000000000000000000c0ffee01")
	slot := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000007")

	buf := NewPlainStateBuffer()
	buf.storage[addr] = map[types.Hash]storageEntry{
		slot: storageEntryFromBytes([]byte{0x42, 0x00, 0x00, 0x99}),
	}

	db := memdb.NewTestDB(b)
	roTx, err := db.BeginRo(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	defer roTx.Rollback()
	r := NewBufferedPlainStateReader(buf, roTx)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = r.ReadAccountStorage(addr, &slot)
	}
}

// BenchmarkReadAccountStorage_LRUHit measures the LRU hit path. After
// the alloc fix this path STILL allocates 0 bytes — ck is a stack
// array, no heap escape. The bench guards against future regressions
// where someone introduces an alloc.
func BenchmarkReadAccountStorage_LRUHit(b *testing.B) {
	addr := types.HexToAddress("0x00000000000000000000000000000000c0ffee02")
	slot := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000008")

	buf := NewPlainStateBuffer()
	// Direct LRU populate (skip buf.storage).
	var ck [storageCompositeKeyLen]byte
	copy(ck[:20], addr[:])
	copy(ck[20:], slot[:])
	val := []byte{0x12, 0x34}
	buf.readStorage.Put(ck, val, storageCompositeKeyLen+len(val)+cacheOverheadPerEntry)

	db := memdb.NewTestDB(b)
	roTx, err := db.BeginRo(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	defer roTx.Rollback()
	r := NewBufferedPlainStateReader(buf, roTx)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = r.ReadAccountStorage(addr, &slot)
	}
}

// BenchmarkReadAccountStorage_MDBXMissNegative is the MDBX miss path
// for an absent slot (returns nil and caches the negative entry).
//
// This is the path the alloc-fix targeted: pre-fix it called
// PlainGenerateCompositeStorageKey to build a fresh 52-byte slice with
// content identical to the just-built `ck` array. Post-fix it reuses
// ck[:]. Should show 1 fewer alloc per call (drops from 2 allocs to 1
// — the remaining alloc is the LRU's negative-cache Put internal).
//
// The first call hits MDBX, populates negative cache; subsequent calls
// (b.N-1) hit the LRU. To benchmark the actual MDBX path repeatedly,
// reset the cache each iter with b.StopTimer/StartTimer or use a
// fresh slot per iter — we use a fresh-slot pattern via i.
func BenchmarkReadAccountStorage_MDBXMissNegative(b *testing.B) {
	addr := types.HexToAddress("0x00000000000000000000000000000000c0ffee03")

	buf := NewPlainStateBuffer()
	db := memdb.NewTestDB(b)
	roTx, err := db.BeginRo(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	defer roTx.Rollback()
	r := NewBufferedPlainStateReader(buf, roTx)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// Vary the slot to keep hitting MDBX (avoid LRU negative-cache hit).
		var slot types.Hash
		slot[24] = byte(i)
		slot[25] = byte(i >> 8)
		slot[26] = byte(i >> 16)
		slot[27] = byte(i >> 24)
		_, _ = r.ReadAccountStorage(addr, &slot)
	}
}

// BenchmarkReadAccountStorage_MDBXHitFresh exercises the seeded-MDBX
// path: every iteration uses a slot that was Put in MDBX before the
// timer started. Pre-fix: 3 allocs (PlainGenerate-compositeKey,
// cached buffer, LRU entry). Post-fix: 2 allocs (cached, LRU entry —
// the compositeKey alloc is gone).
func BenchmarkReadAccountStorage_MDBXHitFresh(b *testing.B) {
	addr := types.HexToAddress("0x00000000000000000000000000000000c0ffee04")

	db := memdb.NewTestDB(b)

	// Seed N slots in MDBX, all with the same value.
	{
		rwTx, err := db.BeginRw(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		val := []byte{0x42, 0x42}
		for i := 0; i < b.N; i++ {
			var slot types.Hash
			slot[24] = byte(i)
			slot[25] = byte(i >> 8)
			slot[26] = byte(i >> 16)
			slot[27] = byte(i >> 24)
			key := modules.PlainGenerateCompositeStorageKey(addr[:], slot[:])
			if err := rwTx.Put(modules.Storage, key, val); err != nil {
				b.Fatal(err)
			}
		}
		if err := rwTx.Commit(); err != nil {
			b.Fatal(err)
		}
	}

	buf := NewPlainStateBuffer()
	roTx, err := db.BeginRo(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	defer roTx.Rollback()
	r := NewBufferedPlainStateReader(buf, roTx)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var slot types.Hash
		slot[24] = byte(i)
		slot[25] = byte(i >> 8)
		slot[26] = byte(i >> 16)
		slot[27] = byte(i >> 24)
		_, _ = r.ReadAccountStorage(addr, &slot)
	}
}
