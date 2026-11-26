// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
)

func budgetTestBlock(number uint64, ntx int) *block.Block {
	txs := make([]*transaction.Transaction, ntx)
	for i := range txs {
		txs[i] = transaction.NewTx(&transaction.LegacyTx{Nonce: uint64(i), Gas: 21000, Value: uint256.NewInt(1)})
	}
	h := &block.Header{Number: uint256.NewInt(number), GasLimit: 30_000_000}
	return block.NewBlock(h, txs).(*block.Block)
}

// TestBlockLRUTxBudget: blocks of 1,200 transactions under a 5,000-tx budget
// keep only the newest four, however many are added -- the cache must not
// grow with the chain up to its block-count limit.
func TestBlockLRUTxBudget(t *testing.T) {
	c := newBlockLRU(512, 5000, 2)
	var blks []*block.Block
	for n := uint64(1); n <= 100; n++ {
		b := budgetTestBlock(n, 1200)
		blks = append(blks, b)
		c.Add(b.Hash(), b)
		if c.Txs() > 5000 {
			t.Fatalf("after block %d: %d txs held, budget 5000", n, c.Txs())
		}
	}
	if c.Len() != 4 || c.Txs() != 4*1200 {
		t.Fatalf("Len=%d Txs=%d, want 4 / 4800", c.Len(), c.Txs())
	}
	for i, b := range blks {
		if want := i >= len(blks)-4; c.Contains(b.Hash()) != want {
			t.Fatalf("block %d: Contains=%v want %v", i+1, !want, want)
		}
	}
}

// TestBlockLRUMinBlocks: a block bigger than the whole budget is still kept,
// and so are minBlocks of them, so commit-to-canonical keeps its cache hit on
// a large block.
func TestBlockLRUMinBlocks(t *testing.T) {
	c := newBlockLRU(512, 1000, 3)
	for n := uint64(1); n <= 10; n++ {
		b := budgetTestBlock(n, 2000)
		c.Add(b.Hash(), b)
		if _, ok := c.Get(b.Hash()); !ok {
			t.Fatalf("block %d evicted by its own Add", n)
		}
	}
	if c.Len() != 3 || c.Txs() != 6000 {
		t.Fatalf("Len=%d Txs=%d, want 3 / 6000", c.Len(), c.Txs())
	}
}

// TestBlockLRUAccounting: re-adding a hash and count-limit evictions keep the
// transaction tally exact.
func TestBlockLRUAccounting(t *testing.T) {
	c := newBlockLRU(3, 0, 0) // count-bounded only
	var last *block.Block
	for n := uint64(1); n <= 5; n++ {
		last = budgetTestBlock(n, int(n))
		c.Add(last.Hash(), last)
	}
	c.Add(last.Hash(), last) // replace in place
	if c.Len() != 3 || c.Txs() != 3+4+5 {
		t.Fatalf("Len=%d Txs=%d, want 3 / 12", c.Len(), c.Txs())
	}
}
