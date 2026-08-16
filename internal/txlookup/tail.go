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
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// Tail is the in-memory newest tier of the lookup.
type Tail struct {
	mu sync.RWMutex
	// byHash covers every block currently held. Blocks are added in order and
	// dropped from the oldest end, so an entry is removed exactly when its
	// block leaves.
	byHash map[types.Hash]uint64
	// blocks are consecutive and ascending; blocks[0] is at firstBlock.
	blocks     [][]types.Hash
	firstBlock uint64
	txCount    int
}

// NewTail returns an empty tail.
func NewTail() *Tail {
	return &Tail{byHash: make(map[types.Hash]uint64)}
}

// Add records a block's transaction hashes, in the block's own order.
//
// A block that is not the immediate successor of the last one resets the tail:
// the tail's whole value is that it covers a contiguous range, so a gap would
// make "not in the tail" stop meaning "older than the tail" and lookups would
// silently fall through to segments that do not have it either.
func (t *Tail) Add(number uint64, hashes []types.Hash) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.blocks) == 0 {
		t.firstBlock = number
	} else if number != t.firstBlock+uint64(len(t.blocks)) {
		t.reset()
		t.firstBlock = number
	}
	held := make([]types.Hash, len(hashes))
	copy(held, hashes)
	t.blocks = append(t.blocks, held)
	t.txCount += len(held)
	for _, h := range held {
		t.byHash[h] = number
	}
}

func (t *Tail) reset() {
	t.blocks = nil
	t.txCount = 0
	t.byHash = make(map[types.Hash]uint64)
}

// Lookup returns the block holding txHash, if the tail covers it.
func (t *Tail) Lookup(txHash types.Hash) (uint64, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	n, ok := t.byHash[txHash]
	return n, ok
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
	t.mu.RLock()
	defer t.mu.RUnlock()

	sealable := len(t.blocks) - keepBlocks
	if sealable <= 0 {
		return 0, 0, false
	}
	acc := 0
	for i := 0; i < sealable; i++ {
		acc += len(t.blocks[i])
		if acc >= minTx || i+1 >= maxBlocks {
			return t.firstBlock, t.firstBlock + uint64(i+1), true
		}
	}
	return 0, 0, false
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
		return t.blocks[i], nil
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
		for _, h := range t.blocks[i] {
			// Only if it still maps to this block: a re-org that re-added the
			// same transaction under a newer number must keep the newer entry.
			if t.byHash[h] == t.firstBlock+uint64(i) {
				delete(t.byHash, h)
			}
		}
		t.txCount -= len(t.blocks[i])
	}
	t.blocks = append([][]types.Hash(nil), t.blocks[drop:]...)
	t.firstBlock += uint64(drop)
	if len(t.blocks) == 0 {
		t.reset()
	}
}
