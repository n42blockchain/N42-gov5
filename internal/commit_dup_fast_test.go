package internal

import (
	"context"
	"testing"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// The commit tests hold with the pre-read on.
func TestCommitDupFastKeepsCommitBehaviour(t *testing.T) {
	defer SetCommitDupFastForTest(true)()
	t.Run("heads", TestCommitToCanonicalAdvancesBlockAndHeaderHeads)
	t.Run("hook", TestCommitToCanonicalFiresOnBlockCommittedHook)
	t.Run("nilHook", TestCommitToCanonicalNilHookIsNoop)
	t.Run("duplicateLookup", TestCommitToCanonicalDuplicateTxLookupFollowsIndexer)
}

// A duplicate commit is settled on the read transaction only when nothing
// is left to write: the block canonical at or below the head, the tail
// indexer attached, and no own-unverified mark.
func TestCommitPreReadDuplicate(t *testing.T) {
	defer SetCommitDupFastForTest(true)()
	db := newRealignTestDB(t)
	to := types.HexToAddress("0x200")
	txn := transaction.NewTransaction(0, types.HexToAddress("0x100"), &to, uint256.NewInt(1), 21000, uint256.NewInt(1), nil)
	mk := func(n uint64, parent types.Hash, root byte) *block.Block {
		return block.NewBlock(&block.Header{
			Number:     uint256.NewInt(n),
			ParentHash: parent,
			Difficulty: uint256.NewInt(1),
			Root:       types.Hash{root},
		}, []*transaction.Transaction{txn}).(*block.Block)
	}
	b9 := mk(9, types.Hash{}, 0x09)
	b10 := mk(10, b9.Hash(), 0x0a)
	side := mk(9, types.Hash{}, 0x77)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for _, b := range []*block.Block{b9, b10, side} {
			if err := rawdb.WriteBlock(tx, b); err != nil {
				return err
			}
		}
		if err := rawdb.WriteCanonicalHash(tx, b9.Hash(), 9); err != nil {
			return err
		}
		rawdb.WriteHeadBlockHash(tx, b9.Hash())
		return rawdb.WriteHeadHeaderHash(tx, b9.Hash())
	}); err != nil {
		t.Fatal(err)
	}
	bc := &BlockChain{ChainDB: db, ctx: context.Background()}
	bc.currentBlock.Store(b9)

	// No indexer: the duplicate still has lookups to repair.
	if dup, _, blk, err := bc.commitPreRead(b9.Hash()); err != nil || dup || blk == nil || blk.Hash() != b9.Hash() {
		t.Fatalf("no indexer: dup=%v blk=%v err=%v, want the block", dup, blk != nil, err)
	}
	bc.SetTxIndexer(&testTxIndexer{})
	if dup, n, blk, err := bc.commitPreRead(b9.Hash()); err != nil || !dup || n != 9 || blk != nil {
		t.Fatalf("canonical head: dup=%v n=%d blk=%v err=%v, want dup at 9", dup, n, blk != nil, err)
	}
	// A mark left to clear goes through the write transaction.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return rawdb.WriteOwnUnverifiedMark(tx, b9.Hash(), 9)
	}); err != nil {
		t.Fatal(err)
	}
	if dup, _, blk, err := bc.commitPreRead(b9.Hash()); err != nil || dup || blk == nil {
		t.Fatalf("marked: dup=%v blk=%v err=%v", dup, blk != nil, err)
	}
	if err := bc.CommitToCanonical(b9.Hash()); err != nil {
		t.Fatal(err)
	}
	if dup, _, _, err := bc.commitPreRead(b9.Hash()); err != nil || !dup {
		t.Fatalf("mark cleared: dup=%v err=%v", dup, err)
	}
	// Above the head, a sibling, and an unknown hash are not duplicates.
	if dup, _, blk, err := bc.commitPreRead(b10.Hash()); err != nil || dup || blk == nil {
		t.Fatalf("above head: dup=%v blk=%v err=%v", dup, blk != nil, err)
	}
	if dup, _, blk, err := bc.commitPreRead(side.Hash()); err != nil || dup || blk == nil {
		t.Fatalf("sibling: dup=%v blk=%v err=%v", dup, blk != nil, err)
	}
	if dup, _, blk, err := bc.commitPreRead(types.Hash{0xff}); err != nil || dup || blk != nil {
		t.Fatalf("unknown: dup=%v blk=%v err=%v", dup, blk != nil, err)
	}
	// The new head commits through the write transaction as before, and its
	// repeat is then a duplicate.
	var committed []uint64
	bc.SetOnBlockCommitted(func(number uint64) { committed = append(committed, number) })
	for i := 0; i < 2; i++ {
		if err := bc.CommitToCanonical(b10.Hash()); err != nil {
			t.Fatal(err)
		}
	}
	if len(committed) != 1 || committed[0] != 10 || bc.CurrentBlock().Hash() != b10.Hash() {
		t.Fatalf("commit callback = %v head = %s", committed, bc.CurrentBlock().Hash())
	}
	if dup, n, _, err := bc.commitPreRead(b10.Hash()); err != nil || !dup || n != 10 {
		t.Fatalf("repeat: dup=%v n=%d err=%v", dup, n, err)
	}
	if dup, _, _, err := bc.commitPreRead(b9.Hash()); err != nil || !dup {
		t.Fatalf("below head: dup=%v err=%v", dup, err)
	}
}

// TestCommitUsesBlockCachedAfterPreRead covers a commit that arrives before
// its block is stored: the pre-read misses, the block's write lands while the
// commit waits for the writer, and the write transaction must then take the
// cached instance (or, without one, still read the block from the database).
func TestCommitUsesBlockCachedAfterPreRead(t *testing.T) {
	defer SetCommitDupFastForTest(true)()
	for _, withCache := range []bool{true, false} {
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
			if err := rawdb.WriteCanonicalHash(tx, parent.Hash(), 8); err != nil {
				return err
			}
			rawdb.WriteHeadBlockHash(tx, parent.Hash())
			return rawdb.WriteHeadHeaderHash(tx, parent.Hash())
		}); err != nil {
			t.Fatal(err)
		}
		cache, _ := lru.New[types.Hash, *block.Block](8)
		bc := &BlockChain{ChainDB: db, ctx: context.Background(), blockCache: cache}
		bc.currentBlock.Store(parent)
		ran := 0
		commitAfterPreReadForTest = func() {
			ran++
			if err := db.Update(context.Background(), func(tx kv.RwTx) error {
				return rawdb.WriteBlock(tx, child)
			}); err != nil {
				t.Fatal(err)
			}
			if withCache {
				cache.Add(child.Hash(), child)
			}
		}
		err := bc.CommitToCanonical(child.Hash())
		commitAfterPreReadForTest = nil
		if err != nil {
			t.Fatalf("cache=%v: %v", withCache, err)
		}
		if ran != 1 {
			t.Fatalf("cache=%v: pre-read hook ran %d times, want 1", withCache, ran)
		}
		if got := bc.CurrentBlock().Hash(); got != child.Hash() {
			t.Fatalf("cache=%v: head = %s, want %s", withCache, got, child.Hash())
		}
	}
}
