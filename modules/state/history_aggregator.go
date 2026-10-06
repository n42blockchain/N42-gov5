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
	"sync"

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

// AddKey records one (key, block) directly, for callers that read changeset
// rows back from the database rather than holding a ChangeSet. The backfiller
// does exactly that: the index is a pure function of the changesets, so it
// rebuilds from the durable rows instead of having data handed out of the
// commit transaction.
func (a *HistoryAggregator) AddKey(bucket string, key []byte, blockNum uint64) {
	m := a.accounts
	if bucket == modules.StorageHistory {
		m = a.storage
	}
	a.add(m, key, blockNum)
}

// HistoryIndexMu serialises the writers of the history index tables that read
// the index before they write it. The deferred fold prepares its rows under a
// read transaction and writes them in a later write transaction; a prune landing
// in between would have its deletions undone by the fold's rewrite of a key's
// last chunk. The fold and the pruner both hold this lock across their read and
// their write. The inline path (Flush inside the block's own write transaction)
// needs no lock: MDBX already serialises it against both.
var HistoryIndexMu sync.Mutex

// Flush merges the accumulated block numbers into the on-disk history indices —
// one read+union+chunked-write per distinct key, in sorted key order — and
// resets the aggregator for the next batch.
func (a *HistoryAggregator) Flush(rwTx kv.RwTx) error {
	p, err := a.Prepare(rwTx)
	if err != nil {
		return err
	}
	return p.Apply(rwTx)
}

// PreparedHistory holds every index row a flush puts, computed ahead of the
// write transaction, in the order the flush puts them.
type PreparedHistory struct {
	rows []preparedHistoryRow
}

type preparedHistoryRow struct {
	bucket     string
	key, value []byte
}

// Len reports how many rows Apply puts.
func (p *PreparedHistory) Len() int { return len(p.rows) }

// Prepare is the read side of Flush, against tx, which may be read-only: for
// each distinct key, in sorted order, it reads the key's last chunk, unions the
// batch's block numbers in and encodes the resulting chunks. It resets the
// aggregator. The rows are only valid for a write transaction that sees the
// same index content as tx; a caller that prepares outside the write
// transaction guarantees that with HistoryIndexMu.
//
// Reading every key before writing any is equivalent to Flush's interleaving:
// history keys in one table have a fixed length, so one key's chunks are never
// in another key's read range.
//
// The split exists for the deferred fold. Inside the write transaction the reads
// and encodes of ~46k hot accounts held the MDBX writer 0.6-8.5 s every 20 s on
// the qs fleet (round 35zzo), and the block writes queued behind it.
func (a *HistoryAggregator) Prepare(tx kv.Tx) (*PreparedHistory, error) {
	p := &PreparedHistory{rows: make([]preparedHistoryRow, 0, len(a.accounts)+len(a.storage))}
	if err := prepareHistoryMap(tx, modules.AccountsHistory, a.accounts, p); err != nil {
		return nil, err
	}
	if err := prepareHistoryMap(tx, modules.StorageHistory, a.storage, p); err != nil {
		return nil, err
	}
	a.accounts = make(map[string]*roaring64.Bitmap)
	a.storage = make(map[string]*roaring64.Bitmap)
	return p, nil
}

// Apply puts the prepared rows.
func (p *PreparedHistory) Apply(rwTx kv.RwTx) error {
	for _, r := range p.rows {
		if err := rwTx.Put(r.bucket, r.key, r.value); err != nil {
			return err
		}
	}
	return nil
}

func prepareHistoryMap(tx kv.Tx, bucket string, m map[string]*roaring64.Bitmap, p *PreparedHistory) error {
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
		index, err := bitmapdb.Get64(tx, bucket, []byte(k), math.MaxUint32, math.MaxUint32)
		if err != nil {
			return err
		}
		index.Or(m[k])
		if err = bitmapdb.WalkChunkWithKeys64([]byte(k), index, bitmapdb.ChunkLimit, func(chunkKey []byte, chunk *roaring64.Bitmap) error {
			buf.Reset()
			if _, err := chunk.WriteTo(buf); err != nil {
				return err
			}
			p.rows = append(p.rows, preparedHistoryRow{bucket: bucket, key: chunkKey, value: types.CopyBytes(buf.Bytes())})
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
