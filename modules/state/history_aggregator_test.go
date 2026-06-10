// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/changeset"
)

// fabricated per-block changesets: a hot key touched every block (coinbase
// pattern), warm keys touched periodically, and unique cold keys — across
// enough blocks to make per-block vs batched aggregation diverge if wrong.
func historyWorkload(nBlocks int) map[uint64]*changeset.ChangeSet {
	out := make(map[uint64]*changeset.ChangeSet, nBlocks)
	addr := func(i int) []byte {
		k := make([]byte, 20)
		k[0], k[1], k[2] = byte(i>>16), byte(i>>8), byte(i)
		return k
	}
	for b := 1; b <= nBlocks; b++ {
		cs := changeset.NewAccountChangeSet()
		_ = cs.Add(addr(0), []byte{1}) // hot: every block
		if b%5 == 0 {
			_ = cs.Add(addr(1), []byte{2}) // warm
		}
		_ = cs.Add(addr(1000+b), []byte{3}) // cold: unique per block
		out[uint64(b)] = cs
	}
	return out
}

func dumpTable(t *testing.T, tx kv.Tx, table string) map[string][]byte {
	t.Helper()
	m := map[string][]byte{}
	c, err := tx.Cursor(table)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for k, v, e := c.First(); k != nil && e == nil; k, v, e = c.Next() {
		m[string(k)] = append([]byte(nil), v...)
	}
	return m
}

// TestHistoryAggregatorEquivalence: flushing the batch aggregator must produce
// BYTE-IDENTICAL history tables to the legacy per-block writeIndex path,
// including across pre-existing index content (a second batch on top of the
// first) and the chunk-walk encoding.
func TestHistoryAggregatorEquivalence(t *testing.T) {
	const nBlocks = 400
	work := historyWorkload(nBlocks)

	_, legacyTx := memdb.NewTestTx(t)
	_, aggTx := memdb.NewTestTx(t)

	runBatch := func(from, to uint64) {
		// legacy: per-block read-modify-write
		for b := from; b <= to; b++ {
			if err := writeIndex(b, work[b], modules.AccountsHistory, legacyTx); err != nil {
				t.Fatalf("writeIndex block %d: %v", b, err)
			}
		}
		// aggregated: accumulate then flush once
		agg := NewHistoryAggregator()
		for b := from; b <= to; b++ {
			agg.AddChanges(b, work[b], modules.AccountsHistory)
		}
		if err := agg.Flush(aggTx); err != nil {
			t.Fatalf("agg flush: %v", err)
		}
	}
	// Two batches: the second exercises merging into PRE-EXISTING bitmaps.
	runBatch(1, 250)
	runBatch(251, nBlocks)

	la := dumpTable(t, legacyTx, modules.AccountsHistory)
	ag := dumpTable(t, aggTx, modules.AccountsHistory)
	if len(la) == 0 {
		t.Fatal("legacy produced no rows")
	}
	if len(la) != len(ag) {
		t.Fatalf("row count diverged: legacy=%d agg=%d", len(la), len(ag))
	}
	for k, v := range la {
		if !bytes.Equal(v, ag[k]) {
			t.Fatalf("row %x diverged:\n legacy=%x\n agg   =%x", k, v, ag[k])
		}
	}
}

// TestHistoryAggregatorChunkSplit: a key with enough blocks to exceed the
// bitmap chunk limit must produce the identical multi-chunk layout via both
// paths (chunking depends only on final content).
func TestHistoryAggregatorChunkSplit(t *testing.T) {
	_, legacyTx := memdb.NewTestTx(t)
	_, aggTx := memdb.NewTestTx(t)

	key := make([]byte, 20)
	key[0] = 0xaa
	const nBlocks = 30000 // enough sparse adds to overflow one ~1950B chunk

	agg := NewHistoryAggregator()
	for b := uint64(1); b <= nBlocks; b++ {
		cs := changeset.NewAccountChangeSet()
		_ = cs.Add(key, []byte{1})
		// sparse pattern resists run-length compression -> forces chunk split
		blk := b * 7
		csb := changeset.NewAccountChangeSet()
		_ = csb.Add(key, []byte{1})
		if err := writeIndex(blk, csb, modules.AccountsHistory, legacyTx); err != nil {
			t.Fatal(err)
		}
		agg.AddChanges(blk, cs, modules.AccountsHistory)
	}
	if err := agg.Flush(aggTx); err != nil {
		t.Fatal(err)
	}
	la := dumpTable(t, legacyTx, modules.AccountsHistory)
	ag := dumpTable(t, aggTx, modules.AccountsHistory)
	if len(la) < 2 {
		t.Fatalf("expected multi-chunk layout, got %d rows", len(la))
	}
	if fmt.Sprint(len(la)) != fmt.Sprint(len(ag)) {
		t.Fatalf("chunk count diverged: %d vs %d", len(la), len(ag))
	}
	for k, v := range la {
		if !bytes.Equal(v, ag[k]) {
			t.Fatalf("chunk %x diverged", k)
		}
	}
}
