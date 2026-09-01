// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// HistoryAggregator batches inverted-index updates (AccountsHistory /
// StorageHistory) across many blocks. The per-block writeIndex pattern pays a
// full read-decode-add-encode-write roaring round-trip PER CHANGED KEY PER
// BLOCK — a hot key (the coinbase account is touched by EVERY block) repeats
// that cycle thousands of times within one replay batch. Profiling a full-chain
// conversion attributed ~330 GB of allocations to this path (roaring clone/
// decode/insert + writeIndex), plus one MDBX read and one write per key-block.
//
// The aggregator instead accumulates the block numbers per key in memory for
// the whole batch and flushes ONCE per distinct key: one bitmap read, one
// union, one chunked write — turning k touches of a key into 1 round-trip.
// Keys are flushed in sorted order so the B+tree sees near-sequential writes.
//
// Correctness: nothing reads the history tables mid-replay (historical-state
// queries and unwind use changesets), and the flush happens inside the same
// MDBX transaction as the per-block writes, so crash atomicity is unchanged.
// The resulting table bytes are identical to the per-block path because chunk
// splitting depends only on the final bitmap content (asserted by tests).

package state

import (
	"bytes"
	"math"
	"sort"

	"github.com/RoaringBitmap/roaring/roaring64"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/changeset"
	"github.com/n42blockchain/N42/modules/ethdb/bitmapdb"
)

// HistoryAggregator accumulates history-index block numbers per key across a
// batch of blocks. Not safe for concurrent use.
type HistoryAggregator struct {
	accounts map[string]*roaring64.Bitmap
	storage  map[string]*roaring64.Bitmap
}

// NewHistoryAggregator creates an empty aggregator.
func NewHistoryAggregator() *HistoryAggregator {
	return &HistoryAggregator{
		accounts: make(map[string]*roaring64.Bitmap),
		storage:  make(map[string]*roaring64.Bitmap),
	}
}

func (a *HistoryAggregator) add(m map[string]*roaring64.Bitmap, key []byte, block uint64) {
	bm, ok := m[string(key)]
	if !ok {
		bm = roaring64.New()
		m[string(key)] = bm
	}
	bm.Add(block)
}

// AddChanges records one block's changeset into the aggregator, keyed the
// same way as writeIndex.
func (a *HistoryAggregator) AddChanges(blockNum uint64, changes *changeset.ChangeSet, bucket string) {
	m := a.accounts
	if bucket == modules.StorageHistory {
		m = a.storage
	}
	for _, change := range changes.Changes {
		a.add(m, change.Key, blockNum)
	}
}

// Flush merges the accumulated block numbers into the on-disk history indices —
// one read+union+chunked-write per distinct key, in sorted key order — and
// resets the aggregator for the next batch.
func (a *HistoryAggregator) Flush(rwTx kv.RwTx) error {
	if err := flushHistoryMap(rwTx, modules.AccountsHistory, a.accounts); err != nil {
		return err
	}
	if err := flushHistoryMap(rwTx, modules.StorageHistory, a.storage); err != nil {
		return err
	}
	a.accounts = make(map[string]*roaring64.Bitmap)
	a.storage = make(map[string]*roaring64.Bitmap)
	return nil
}

func flushHistoryMap(rwTx kv.RwTx, bucket string, m map[string]*roaring64.Bitmap) error {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buf := bytes.NewBuffer(nil)
	for _, k := range keys {
		index, err := bitmapdb.Get64(rwTx, bucket, []byte(k), math.MaxUint32, math.MaxUint32)
		if err != nil {
			return err
		}
		index.Or(m[k])
		if err = bitmapdb.WalkChunkWithKeys64([]byte(k), index, bitmapdb.ChunkLimit, func(chunkKey []byte, chunk *roaring64.Bitmap) error {
			buf.Reset()
			if _, err := chunk.WriteTo(buf); err != nil {
				return err
			}
			return rwTx.Put(bucket, chunkKey, types.CopyBytes(buf.Bytes()))
		}); err != nil {
			return err
		}
	}
	return nil
}
