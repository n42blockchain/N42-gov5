// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// blockCacheMaxTxs bounds bc.blockCache by the number of decoded transactions
// it retains, on top of the blockCacheLimit block count.
//
// A count of blocks alone retains memory proportional to block size: at a
// steady 1,200 transactions per block the 512-block limit held ~0.35 MB of
// decoded transactions per block and filled for 68 minutes (P50 round,
// 2026-11-26: live heap rising ~10 MB/min, RSS ~25 MB/min at GOGC 100); at
// the bench fleet's 22,857-transaction blocks it is gigabytes. 65,536
// transactions is ~25 MB of decoded transactions, and still covers dozens of
// ordinary blocks for the readers that need them (commit-to-canonical two
// views later, GetBlock on the recent head).
var blockCacheMaxTxs = envInt("N42_BLOCK_CACHE_TXS", 1<<16)

// blockCacheMinBlocks is the number of most recent blocks kept regardless of
// the transaction budget: commit-to-canonical looks up the block it imported
// two views earlier, and dropping it would put an MDBX decode of a large
// block back on the HotStuff commit path.
const blockCacheMinBlocks = 8

// blockLRU is an LRU of decoded blocks bounded both by entry count and by the
// total number of transactions held.
type blockLRU struct {
	mu        sync.Mutex // serialises Add and its trim; Get/Contains are lock-free in lru
	cache     *lru.Cache[types.Hash, *block.Block]
	txs       int // transactions held; updated only under mu (evictions happen inside Add)
	maxTxs    int
	minBlocks int
}

func newBlockLRU(maxBlocks, maxTxs, minBlocks int) *blockLRU {
	c := &blockLRU{maxTxs: maxTxs, minBlocks: minBlocks}
	// lru.NewWithEvict calls the callback synchronously inside Add /
	// RemoveOldest, both of which run under c.mu, so c.txs needs no atomics.
	c.cache, _ = lru.NewWithEvict(maxBlocks, func(_ types.Hash, b *block.Block) {
		c.txs -= blockTxCount(b)
	})
	return c
}

func blockTxCount(b *block.Block) int {
	if b == nil {
		return 0
	}
	return len(b.Transactions())
}

// Add inserts (or replaces) blk under hash, then evicts the least recently
// used blocks while the transaction budget is exceeded, keeping at least
// minBlocks entries.
func (c *blockLRU) Add(hash types.Hash, blk *block.Block) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.cache.Peek(hash); ok {
		// Replacing a value does not run the evict callback.
		c.txs -= blockTxCount(old)
	}
	c.cache.Add(hash, blk)
	c.txs += blockTxCount(blk)
	for c.maxTxs > 0 && c.txs > c.maxTxs && c.cache.Len() > c.minBlocks {
		if _, _, ok := c.cache.RemoveOldest(); !ok {
			break
		}
	}
}

func (c *blockLRU) Get(hash types.Hash) (*block.Block, bool) { return c.cache.Get(hash) }

func (c *blockLRU) Contains(hash types.Hash) bool { return c.cache.Contains(hash) }

func (c *blockLRU) Len() int { return c.cache.Len() }

// Txs reports the number of transactions currently held.
func (c *blockLRU) Txs() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.txs
}
