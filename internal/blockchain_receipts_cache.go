// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Receipts read cache (geth's BlockChain.receiptsCache): RPC consumers such
// as block explorers ask for the receipts of one block once per transaction
// (eth_getTransactionReceipt) or once per block (eth_getBlockReceipts). Each
// miss decodes the stored receipts and derives their metadata from the body;
// the cache makes the per-transaction path O(1) after the first call instead
// of O(block size).

package internal

import (
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// receiptsCacheBudget bounds the cache by receipt count, not by block count:
// a profile-1 block carries ~1.2k receipts, a bench block 160k+, so a block
// count alone would let a few huge blocks pin gigabytes.
var receiptsCacheBudget = envInt("N42_RECEIPTS_CACHE_RECEIPTS", 131072)

// receiptsCache is an LRU of derived receipts keyed by block hash, bounded by
// the total number of receipts it holds. Cached slices are shared between
// readers and must be treated as read-only.
type receiptsCache struct {
	mu     sync.Mutex
	lru    *lru.Cache[types.Hash, block.Receipts]
	total  int
	budget int
}

func newReceiptsCache(entries, budget int) *receiptsCache {
	c := &receiptsCache{budget: budget}
	c.lru, _ = lru.NewWithEvict[types.Hash, block.Receipts](entries, func(_ types.Hash, r block.Receipts) {
		c.total -= len(r)
	})
	return c
}

func (c *receiptsCache) get(hash types.Hash) (block.Receipts, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Get(hash)
}

// add caches receipts unless one block would take more than half the budget.
func (c *receiptsCache) add(hash types.Hash, receipts block.Receipts) {
	if c == nil || c.budget <= 0 || len(receipts) == 0 || len(receipts) > c.budget/2 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lru.Contains(hash) {
		return
	}
	c.lru.Add(hash, receipts)
	c.total += len(receipts)
	for c.total > c.budget {
		if _, _, ok := c.lru.RemoveOldest(); !ok {
			break
		}
	}
}

func (c *receiptsCache) purge() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lru.Purge()
	c.total = 0
}
