// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// tail.go holds the newest blocks' transaction hashes in memory, so committing
// a block writes no index at all.
//
// The index used to be written as one MDBX row per transaction, keyed by
// transaction hash, inside the write transaction that advances the committed
// head. Measured across 193 committed blocks at a 480M gas ceiling, that
// transaction spent 2,700 ms in mdbx_txn_commit against 149 ms of actual work
// -- roughly 77% of a 3.53 s block cycle. The rows are cheap to stage; the cost
// is that 22,857 keys spread uniformly over the whole chain's keyspace touch
// almost that many separate B-tree leaves, and every one of them has to be
// written out and fsynced.
//
// Moving that write off the consensus path but leaving it in the same store
// only relocated the problem: MDBX has one writer, so the index transaction
// then blocked block building instead, and throughput fell further (6,476 TPS
// to 4,025, with 5 of 17 blocks part-empty while 109,122 executable
// transactions waited). The fix has to be to stop writing a random-key index
// into that store at all.
//
// So: the newest blocks live here, in memory, and are sealed in batches into
// RecSplit segments -- which are built once, written sequentially, and read by
// mmap. Nothing is written per block.
//
// The tail is not durable and does not need to be. It is derivable from blocks
// that are already durable, so a restart rebuilds it by re-reading the bodies
// of the blocks that were not yet sealed. That is the property that makes the
// whole arrangement safe: an index can be reconstructed, a committed head
// cannot.

package txlookup

import (
	"encoding/binary"
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// Tail is the in-memory newest tier of the lookup.
//
// Each block carries its own small hash index instead of one map over the
// whole tail. A single map[hash]block never shrinks: Go maps keep their peak
// bucket array after deletes, so a tail that once grew to 20M entries during
// a seal backlog held ~2 GB for the rest of the run (round S97: 2.04 GB under
// Tail.Add at +150 s). A per-block index is released with its block, costs
// ~37 bytes a transaction instead of ~100, and a lookup probes the held
// blocks newest first -- a few dozen blocks, a handful of compares each.
type Tail struct {
	mu sync.RWMutex
	// blocks are consecutive and ascending; blocks[0] is at firstBlock.
	blocks     []*tailEntry
	firstBlock uint64
	txCount    int
}

// tailEntry is one block's hashes in block order plus a bucket index over
// them: bucket b holds perm[offs[b]:offs[b+1]], the positions whose hash's
// top `bits` bits are b.
type tailEntry struct {
	hashes []types.Hash
	offs   []uint32
	perm   []uint32
	bits   uint8
}

// tailBucketLoad is the average bucket occupancy the index is sized for.
const tailBucketLoad = 4

func newTailEntry(hashes []types.Hash) *tailEntry {
	e := &tailEntry{hashes: hashes}
	n := len(hashes)
	if n == 0 {
		return e
	}
	for e.bits < 20 && (1<<e.bits)*tailBucketLoad < n {
		e.bits++
	}
	nb := 1 << e.bits
	e.offs = make([]uint32, nb+1)
	for i := range hashes {
		e.offs[e.bucket(&hashes[i])+1]++
	}
	for b := 1; b <= nb; b++ {
		e.offs[b] += e.offs[b-1]
	}
	fill := make([]uint32, nb)
	copy(fill, e.offs[:nb])
	e.perm = make([]uint32, n)
	for i := range hashes {
		b := e.bucket(&hashes[i])
		e.perm[fill[b]] = uint32(i)
		fill[b]++
	}
	return e
}

func (e *tailEntry) bucket(h *types.Hash) uint32 {
	// A shift by 32 yields 0 in Go, which is the single bucket of bits == 0.
	return binary.BigEndian.Uint32(h[:4]) >> (32 - uint32(e.bits))
}

func (e *tailEntry) has(h *types.Hash) bool {
	if len(e.hashes) == 0 {
		return false
	}
	b := e.bucket(h)
	for _, i := range e.perm[e.offs[b]:e.offs[b+1]] {
		if e.hashes[i] == *h {
			return true
		}
	}
	return false
}

// NewTail returns an empty tail.
func NewTail() *Tail {
	return &Tail{}
}

// Add records a block's transaction hashes, in the block's own order.
//
// A block that is not the immediate successor of the last one resets the tail:
// the tail's whole value is that it covers a contiguous range, so a gap would
// make "not in the tail" stop meaning "older than the tail" and lookups would
// silently fall through to segments that do not have it either.
func (t *Tail) Add(number uint64, hashes []types.Hash) {
	held := make([]types.Hash, len(hashes))
	copy(held, hashes)
	t.AddOwned(number, held)
}

// AddOwned is Add for a hashes slice the caller hands over and never touches
// again; it saves the copy.
func (t *Tail) AddOwned(number uint64, hashes []types.Hash) {
	// The index is built outside the lock: it is O(n) work on the block's own
	// hashes and lookups need not wait for it.
	e := newTailEntry(hashes)

	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.blocks) == 0 {
		t.firstBlock = number
	} else if number != t.firstBlock+uint64(len(t.blocks)) {
		t.reset()
		t.firstBlock = number
	}
	t.blocks = append(t.blocks, e)
	t.txCount += len(hashes)
}

func (t *Tail) reset() {
	t.blocks = nil
	t.txCount = 0
}

// Lookup returns the block holding txHash, if the tail covers it. Blocks are
// probed newest first, so a transaction a re-org re-added under a newer
// number resolves to the newer block.
func (t *Tail) Lookup(txHash types.Hash) (uint64, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for i := len(t.blocks) - 1; i >= 0; i-- {
		if t.blocks[i].has(&txHash) {
			return t.firstBlock + uint64(i), true
		}
	}
	return 0, false
}

// FirstBlock is the oldest block the tail holds; ok is false when it is empty.
func (t *Tail) FirstBlock() (uint64, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if len(t.blocks) == 0 {
		return 0, false
	}
	return t.firstBlock, true
}

// Len and TxCount report what the tail is holding.
func (t *Tail) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.blocks)
}

func (t *Tail) TxCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.txCount
}

// SealRange returns the oldest [start, end) worth sealing: the leading blocks
// whose transactions reach minTx, or maxBlocks of them, whichever comes first,
// leaving at least keepBlocks behind.
//
// Blocks are kept behind the seal point because sealing reads their hashes and
// building a segment takes time; a block must stay answerable throughout, and
// the newest blocks are also the ones a caller is most likely to ask about.
//
// The block bound matters as much as the transaction one, and for a different
// reason. A transaction count alone lets the tail grow to any number of blocks
// when blocks are small -- and what a restart costs is set by BLOCKS, not
// transactions: rebuilding re-reads each unsealed block in full to recover its
// hashes, which measured 1.25 GB of allocation, the largest single source in
// the node. Bounding blocks bounds the rebuild whatever the density.
func (t *Tail) SealRange(minTx, maxBlocks, keepBlocks int) (start, end uint64, ok bool) {
	return t.SealRangeKeepTx(minTx, maxBlocks, keepBlocks, 0)
}

// SealRangeKeepTx is SealRange with the blocks kept behind the seal point also
// bounded by transactions: the newest blocks are kept until keepBlocks of them
// or keepTx transactions, whichever comes first, and at least one block always
// stays. keepTx <= 0 leaves the keep bounded by blocks alone.
//
// A block count alone is sized for ordinary blocks. At the qs fleet's 163,000
// transactions a block, 64 kept blocks are 10.4M hashes, ~1.1 GB of a node's
// heap held against a 10 GiB GOMEMLIMIT (round 35zzo), while the newest
// million transactions are what a caller is most likely to ask about.
func (t *Tail) SealRangeKeepTx(minTx, maxBlocks, keepBlocks, keepTx int) (start, end uint64, ok bool) {
	rs := t.SealRanges(minTx, maxBlocks, keepBlocks, keepTx, 1)
	if len(rs) == 0 {
		return 0, 0, false
	}
	return rs[0].Start, rs[0].End, true
}

// BlockRange is a half-open block range [Start, End).
type BlockRange struct{ Start, End uint64 }

// SealRanges returns up to maxRanges consecutive sealable ranges, oldest
// first, each cut exactly as SealRangeKeepTx cuts its one range. A sealer
// that builds them concurrently and appends them in order keeps up with a
// commit rate one builder cannot: a 1M-transaction segment takes ~4 s to
// build, ~250k transactions a second, below the qs fleet's 290k (S97).
func (t *Tail) SealRanges(minTx, maxBlocks, keepBlocks, keepTx, maxRanges int) []BlockRange {
	t.mu.RLock()
	defer t.mu.RUnlock()

	keep := keepBlocks
	if keepTx > 0 {
		held, n := 0, 0
		for i := len(t.blocks) - 1; i >= 0 && n < keepBlocks; i-- {
			if n > 0 && held+len(t.blocks[i].hashes) > keepTx {
				break
			}
			held += len(t.blocks[i].hashes)
			n++
		}
		if n < keep {
			keep = n
		}
	}
	sealable := len(t.blocks) - keep
	var out []BlockRange
	from, acc := 0, 0
	for i := 0; i < sealable && len(out) < maxRanges; i++ {
		acc += len(t.blocks[i].hashes)
		if acc >= minTx || i+1-from >= maxBlocks {
			out = append(out, BlockRange{t.firstBlock + uint64(from), t.firstBlock + uint64(i+1)})
			from, acc = i+1, 0
		}
	}
	return out
}

// Source returns a BlockTxSource over the tail's own blocks, for the sealer.
func (t *Tail) Source() BlockTxSource {
	return func(n uint64) ([]types.Hash, error) {
		t.mu.RLock()
		defer t.mu.RUnlock()
		i := int(n - t.firstBlock)
		if len(t.blocks) == 0 || i < 0 || i >= len(t.blocks) {
			return nil, nil
		}
		return t.blocks[i].hashes, nil
	}
}

// DropBelow releases blocks below number, which the caller must already have
// sealed. Dropping a block that is in no segment makes its transactions
// unfindable, so this is deliberately separate from sealing rather than a side
// effect of it.
func (t *Tail) DropBelow(number uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	drop := int(number - t.firstBlock)
	if drop <= 0 || len(t.blocks) == 0 {
		return
	}
	if drop > len(t.blocks) {
		drop = len(t.blocks)
	}
	for i := 0; i < drop; i++ {
		t.txCount -= len(t.blocks[i].hashes)
	}
	// Copy rather than reslice so the dropped entries' backing slots are not
	// kept reachable.
	t.blocks = append([]*tailEntry(nil), t.blocks[drop:]...)
	t.firstBlock += uint64(drop)
	if len(t.blocks) == 0 {
		t.reset()
	}
}
