// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Regression: a start whose rebuild seals backlog segments must serve
// lookups from those segments immediately, not only after another restart.

package txindexer

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// stubChain serves synthetic blocks whose single transaction hash is
// derivable from the block number, so a test can ask for any height.
type stubChain struct{ head uint64 }

func txForBlock(n uint64) *transaction.Transaction {
	return transaction.NewTx(&transaction.LegacyTx{
		Nonce: n,
		Gas:   21000,
		Value: uint256.NewInt(n),
	})
}

func (c *stubChain) blockAt(n uint64) block.IBlock {
	h := &block.Header{Number: uint256.NewInt(n)}
	return block.NewBlock(h, []*transaction.Transaction{txForBlock(n)})
}

func (c *stubChain) CurrentBlock() block.IBlock { return c.blockAt(c.head) }

func (c *stubChain) GetBlockByNumber(number *uint256.Int) (block.IBlock, error) {
	return c.blockAt(number.Uint64()), nil
}

// TestBacklogSealIsVisibleWithoutRestart pins the hole that cost a
// ~330-block window of eth_getTransactionByHash on the live fleet: Start
// opens the segment reader BEFORE rebuildTxTail writes backlog segments,
// so without an explicit reopen the freshly sealed range resolves nowhere
// until some later restart happens to enumerate it.
func TestBacklogSealIsVisibleWithoutRestart(t *testing.T) {
	t.Setenv("N42_TXINDEX_TAIL", "1")

	// A gap wide enough to force at least one backlog seal.
	const head = txIndexMaxRebuildBlocks*2 + 10
	chain := &stubChain{head: head}
	// The watermark lives in ChainConfig; register the n42 table set first.
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db := memdb.NewTestDB(t)

	// The tier was switched on long ago: a watermark well below the head is
	// what makes Start rebuild, and a gap this wide is what makes it seal
	// backlog segments on the way.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], 1)
		return tx.Put(modules.ChainConfig, txIndexStartKey, buf[:])
	}); err != nil {
		t.Fatal(err)
	}

	x := New(context.Background(), chain, db, t.TempDir())

	if !x.Start() {
		t.Fatal("tail tier failed to start")
	}
	defer x.Stop()

	// A block inside the FIRST backlog segment — the range that used to be
	// invisible. The tail itself only keeps what follows the last seal.
	probe := uint64(txIndexMaxRebuildBlocks / 2)
	want := txForBlock(probe).Hash()
	got, ok := x.LookupTx(want)
	if !ok {
		t.Fatalf("tx of block %d not resolvable right after the sealing start "+
			"(backlog segment written but never reopened)", probe)
	}
	if got != probe {
		t.Fatalf("tx of block %d resolved to block %d", probe, got)
	}
}
