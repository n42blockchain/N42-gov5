// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// headChain is a chain whose only live method is CurrentBlock.
type headChain struct {
	common.IBlockChain
	head atomic.Pointer[block.Block]
}

func (c *headChain) CurrentBlock() block.IBlock {
	if b := c.head.Load(); b != nil {
		return b
	}
	return nil
}

// TestOwnSealedBlocksReleaseDecodedTxs reproduces S97's leader-side leak: a
// lag-built frame block is remembered by the worker (sealedByHash,
// sealedOnParent) and then resolved IN PLACE by the import, which attaches
// every decoded transaction to the same object. Held whole, each block this
// node sealed kept its ~75k decoded transactions for the 512-entry window
// (3.5 GB at +150 s on a 7-node fleet). With the head advancing behind the
// seals, the live heap after a GC must not grow by more than a small
// fraction of one block per sealed block.
func TestOwnSealedBlocksReleaseDecodedTxs(t *testing.T) {
	const (
		blocks   = 60
		warmup   = 20
		txsPerBl = 20_000
		frames   = 40
	)
	ch := &headChain{}
	w := newTestWorkerForSealedBookkeeping()
	w.chain = ch

	heap := func() uint64 {
		runtime.GC()
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.HeapInuse
	}
	to := types.Address{0x42}
	var (
		parent   = types.Hash{0x01}
		perBlock uint64
		base     uint64
	)
	empty := heap()
	for n := uint64(1); n <= blocks; n++ {
		h := testMiningHeader(n)
		h.ParentHash = parent
		fh := make([]types.Hash, frames)
		for i := range fh {
			fh[i] = types.Hash{byte(n), byte(n >> 8), byte(i)}
		}
		blk := block.NewLaggedBlock(h, fh, nil)

		// handleSealed records it, then the seal is remembered.
		w.recordSealedOnParent(parent, blk)
		w.rememberSealed(blk)

		// The import resolves the very same object.
		before := heap()
		txs := make([]*transaction.Transaction, txsPerBl)
		for i := range txs {
			txs[i] = transaction.NewTx(&transaction.LegacyTx{
				Nonce: uint64(i), GasPrice: uint256.NewInt(1), Gas: 21000,
				To: &to, Value: uint256.NewInt(uint64(i)),
				V: uint256.NewInt(27), R: uint256.NewInt(uint64(i) + 1), S: uint256.NewInt(n),
			})
			txs[i].Hash()
		}
		blk.Body().(*block.Body).Txs = txs
		if n == 1 {
			perBlock = heap() - before
		}

		// The head trails the seals by two blocks (the two-chain commit).
		if n > 2 {
			ch.head.Store(block.NewBlock(testMiningHeader(n-2), nil).(*block.Block))
		}
		parent = blk.Hash()
		blk = nil
		if n == warmup {
			base = heap()
		}
	}
	final := heap()
	grown := int64(final) - int64(base)
	t.Logf("one resolved block ~%d KB; heap empty %d MB, after warm-up %d MB, after %d more blocks %d MB",
		perBlock>>10, empty>>20, base>>20, blocks-warmup, final>>20)
	if perBlock == 0 {
		t.Fatal("test setup: a resolved block measured no heap")
	}
	if grown > int64(perBlock)*3 {
		t.Fatalf("heap grew %d KB over %d sealed blocks (one block is %d KB): sealed blocks keep their decoded transactions",
			grown>>10, blocks-warmup, perBlock>>10)
	}
	// What stays whole: the seals above the head (two) -- not the 16 of
	// sealedByHash, nor sealedOnParent's window.
	if held := int64(final) - int64(empty); held > int64(perBlock)*4 {
		t.Fatalf("%d KB held after %d sealed blocks (one block is %d KB): more than the undecided seals",
			held>>10, blocks, perBlock>>10)
	}
	// The newest, undecided seal is still kept whole for a re-proposal.
	if kept := w.firstSealedOnParent(parent); kept != nil {
		t.Fatal("test setup: no block was sealed on the newest hash")
	}
}
