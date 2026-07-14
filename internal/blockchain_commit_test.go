package internal

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func TestCommitToCanonicalAdvancesBlockAndHeaderHeads(t *testing.T) {
	db := newRealignTestDB(t)
	parent := block.NewBlock(&block.Header{
		Number:     uint256.NewInt(8),
		Difficulty: uint256.NewInt(1),
		Root:       types.Hash{0x08},
	}, nil).(*block.Block)
	child := block.NewBlock(&block.Header{
		Number:     uint256.NewInt(9),
		ParentHash: parent.Hash(),
		Difficulty: uint256.NewInt(1),
		Root:       types.Hash{0x09},
	}, nil).(*block.Block)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := rawdb.WriteBlock(tx, parent); err != nil {
			return err
		}
		if err := rawdb.WriteBlock(tx, child); err != nil {
			return err
		}
		if err := rawdb.WriteCanonicalHash(tx, parent.Hash(), 8); err != nil {
			return err
		}
		rawdb.WriteHeadBlockHash(tx, parent.Hash())
		return rawdb.WriteHeadHeaderHash(tx, parent.Hash())
	}); err != nil {
		t.Fatal(err)
	}

	bc := &BlockChain{ChainDB: db, ctx: context.Background()}
	bc.currentBlock.Store(parent)
	if err := bc.CommitToCanonical(child.Hash()); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		if got := rawdb.ReadHeadBlockHash(tx); got != child.Hash() {
			t.Fatalf("block head = %s, want %s", got, child.Hash())
		}
		if got := rawdb.ReadHeadHeaderHash(tx); got != child.Hash() {
			t.Fatalf("header head = %s, want %s", got, child.Hash())
		}
		if got := rawdb.ReadCurrentBlockNumber(tx); got == nil || *got != 9 {
			t.Fatalf("current header number = %v, want 9", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
