// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for the append-only log-index writer: it must produce exactly the
// results the whole-history rewrite produced, stay compatible with data that
// writer already wrote, and keep the chunk-size invariant.

package rawdb

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/ethdb/bitmapdb"
)

func addrN(i int) types.Address {
	var a types.Address
	binary.BigEndian.PutUint32(a[16:], uint32(i))
	return a
}

// bitmapOf reads the full bitmap of key as the sorted block numbers it holds.
func bitmapOf(t *testing.T, tx kv.Tx, bucket string, key []byte) []uint32 {
	t.Helper()
	bm, err := bitmapdb.Get(tx, bucket, key, 0, math.MaxUint32)
	if err != nil {
		t.Fatal(err)
	}
	return bm.ToArray()
}

// TestAppendMatchesRewrite runs the same block stream through the append path
// and through the old whole-history rewrite, and requires identical bitmaps.
func TestAppendMatchesRewrite(t *testing.T) {
	const nBlocks = 4000
	addr := addrN(1)

	dbA := memdb.NewTestDB(t) // append path (production)
	dbR := memdb.NewTestDB(t) // rewrite path (the old behaviour)

	write := func(db kv.RwDB, useRewrite bool) {
		if err := db.Update(context.Background(), func(tx kv.RwTx) error {
			buf := bytes.NewBuffer(nil)
			for n := 1; n <= nBlocks; n++ {
				var err error
				if useRewrite {
					err = rewriteLogIndex(tx, modules.LogAddressIndex, addr[:], uint32(n), buf)
				} else {
					err = addToLogIndex(tx, modules.LogAddressIndex, addr[:], uint32(n), buf)
				}
				if err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	write(dbA, false)
	write(dbR, true)

	var got, want []uint32
	if err := dbA.View(context.Background(), func(tx kv.Tx) error {
		got = bitmapOf(t, tx, modules.LogAddressIndex, addr[:])
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := dbR.View(context.Background(), func(tx kv.Tx) error {
		want = bitmapOf(t, tx, modules.LogAddressIndex, addr[:])
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != nBlocks {
		t.Fatalf("append path holds %d blocks, want %d", len(got), nBlocks)
	}
	if len(got) != len(want) {
		t.Fatalf("cardinality differs: append=%d rewrite=%d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("block %d differs: append=%d rewrite=%d", i, got[i], want[i])
		}
	}
}

// TestAppendRespectsChunkLimit checks that no sealed chunk exceeds ChunkLimit —
// the invariant that keeps MDBX off overflow pages.
func TestAppendRespectsChunkLimit(t *testing.T) {
	db := memdb.NewTestDB(t)
	addr := addrN(2)
	// Sparse numbers compress far worse than a dense run, so this actually
	// crosses the limit and forces seals.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		buf := bytes.NewBuffer(nil)
		for n := 1; n <= 40000; n++ {
			if err := addToLogIndex(tx, modules.LogAddressIndex, addr[:], uint32(n)*97, buf); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	sealed := 0
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		c, err := tx.Cursor(modules.LogAddressIndex)
		if err != nil {
			return err
		}
		defer c.Close()
		for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
			if err != nil {
				return err
			}
			shard := binary.BigEndian.Uint32(k[len(k)-4:])
			if shard == ^uint32(0) {
				continue // the open chunk is allowed to be mid-growth
			}
			sealed++
			if uint64(len(v)) > bitmapdb.ChunkLimit {
				t.Fatalf("sealed chunk %d is %d B, over ChunkLimit %d", shard, len(v), bitmapdb.ChunkLimit)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if sealed == 0 {
		t.Fatal("no chunk was ever sealed — the test did not exercise the seal path")
	}
	t.Logf("sealed chunks: %d", sealed)
}

// TestAppendOverRewrittenData starts a key with the old writer and continues
// with the new one: a datadir written before this change must keep working.
func TestAppendOverRewrittenData(t *testing.T) {
	db := memdb.NewTestDB(t)
	addr := addrN(3)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		buf := bytes.NewBuffer(nil)
		for n := 1; n <= 500; n++ {
			if err := rewriteLogIndex(tx, modules.LogAddressIndex, addr[:], uint32(n), buf); err != nil {
				return err
			}
		}
		for n := 501; n <= 1000; n++ {
			if err := addToLogIndex(tx, modules.LogAddressIndex, addr[:], uint32(n), buf); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		got := bitmapOf(t, tx, modules.LogAddressIndex, addr[:])
		if len(got) != 1000 || got[0] != 1 || got[999] != 1000 {
			t.Fatalf("mixed-writer bitmap wrong: n=%d first=%d last=%d", len(got), got[0], got[len(got)-1])
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestAppendDeepReorgFallback re-adds a block number far below the open chunk's
// floor. The append path must hand off to the rewrite so the number is still
// found afterwards.
func TestAppendDeepReorgFallback(t *testing.T) {
	db := memdb.NewTestDB(t)
	addr := addrN(4)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		buf := bytes.NewBuffer(nil)
		for n := 10000; n <= 12000; n++ {
			if err := addToLogIndex(tx, modules.LogAddressIndex, addr[:], uint32(n), buf); err != nil {
				return err
			}
		}
		// Well below the open chunk's minimum.
		return addToLogIndex(tx, modules.LogAddressIndex, addr[:], 5, buf)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		got := bitmapOf(t, tx, modules.LogAddressIndex, addr[:])
		if len(got) != 2002 || got[0] != 5 {
			t.Fatalf("deep-reorg bitmap wrong: n=%d first=%d", len(got), got[0])
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestWriteLogIndexSortsKeys checks the write order actually reaching the tx.
func TestWriteLogIndexSortsKeys(t *testing.T) {
	db := memdb.NewTestDB(t)
	logs := make([]*block.Log, 0, 64)
	for i := 63; i >= 0; i-- { // fed in descending order on purpose
		logs = append(logs, makeLog(addrN(i)))
	}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return WriteLogIndexFromLogs(tx, 7, logs)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		for i := 0; i < 64; i++ {
			a := addrN(i)
			got := bitmapOf(t, tx, modules.LogAddressIndex, a[:])
			if len(got) != 1 || got[0] != 7 {
				t.Fatalf("addr %d: got %v, want [7]", i, got)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
