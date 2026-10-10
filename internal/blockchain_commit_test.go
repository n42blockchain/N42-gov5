package internal

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
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
	from := types.HexToAddress("0x100")
	to := types.HexToAddress("0x200")
	txn := transaction.NewTransaction(0, from, &to, uint256.NewInt(1), 21000, uint256.NewInt(1), nil)
	child := block.NewBlock(&block.Header{
		Number:     uint256.NewInt(9),
		ParentHash: parent.Hash(),
		Difficulty: uint256.NewInt(1),
		Root:       types.Hash{0x09},
	}, []*transaction.Transaction{txn}).(*block.Block)

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
	var committed []uint64
	bc.SetOnBlockCommitted(func(number uint64) { committed = append(committed, number) })
	if err := bc.CommitToCanonical(child.Hash()); err != nil {
		t.Fatal(err)
	}
	if len(committed) != 1 || committed[0] != 9 {
		t.Fatalf("commit callback = %v, want [9]", committed)
	}
	// A duplicate QC/Decide is idempotent and must not advance consumers twice.
	if err := bc.CommitToCanonical(child.Hash()); err != nil {
		t.Fatal(err)
	}
	if len(committed) != 1 {
		t.Fatalf("duplicate commit fired callback again: %v", committed)
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
		if got, err := rawdb.ReadTxLookupEntry(tx, txn.Hash()); err != nil {
			t.Fatalf("read tx lookup: %v", err)
		} else if got == nil || *got != 9 {
			t.Fatalf("tx lookup block = %v, want 9", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestCommitToCanonicalFiresOnBlockCommittedHook is the regression test for a
// real bug an audit caught before deployment: the hook was originally wired
// only into writeBlockWithState's CanonStatTy branch, which
// canonicalByCommitOnly() (true for leader-driven consensus like HotStuff)
// makes unreachable — that branch returns SideStatTy unconditionally for
// those chains, so a consumer (e.g. the mobile-attestation cohort merge's
// block-height clock) relying on the hook would silently never advance on
// the live HotStuff fleet. CommitToCanonical is HotStuff's actual
// canonicalization point, so the hook must fire from here.
func TestCommitToCanonicalFiresOnBlockCommittedHook(t *testing.T) {
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

	var gotNumbers []uint64
	bc.SetOnBlockCommitted(func(number uint64) { gotNumbers = append(gotNumbers, number) })

	if err := bc.CommitToCanonical(child.Hash()); err != nil {
		t.Fatal(err)
	}

	if len(gotNumbers) != 1 || gotNumbers[0] != 9 {
		t.Fatalf("onBlockCommitted fired with %v, want exactly [9]", gotNumbers)
	}
}

// TestCommitToCanonicalNilHookIsNoop confirms a BlockChain that never calls
// SetOnBlockCommitted (mobileverify disabled, or any consumer that hasn't
// wired it) behaves identically to before this hook existed — no panic, no
// behavior change.
func TestCommitToCanonicalNilHookIsNoop(t *testing.T) {
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
	// No SetOnBlockCommitted call — onBlockCommitted stays nil.
	if err := bc.CommitToCanonical(child.Hash()); err != nil {
		t.Fatalf("commit must succeed with no hook wired: %v", err)
	}
}

// TestCommitToCanonicalDuplicateTxLookupFollowsIndexer pins the duplicate
// (already canonical) commit branch to the normal path's rule: with a tx
// indexer attached no TxLookup rows are written, without one they are.
func TestCommitToCanonicalDuplicateTxLookupFollowsIndexer(t *testing.T) {
	for _, withIndexer := range []bool{true, false} {
		db := newRealignTestDB(t)
		from := types.HexToAddress("0x100")
		to := types.HexToAddress("0x200")
		txn := transaction.NewTransaction(0, from, &to, uint256.NewInt(1), 21000, uint256.NewInt(1), nil)
		child := block.NewBlock(&block.Header{
			Number:     uint256.NewInt(9),
			Difficulty: uint256.NewInt(1),
			Root:       types.Hash{0x09},
		}, []*transaction.Transaction{txn}).(*block.Block)
		// The block is already imported and already the committed head.
		if err := db.Update(context.Background(), func(tx kv.RwTx) error {
			if err := rawdb.WriteBlock(tx, child); err != nil {
				return err
			}
			if err := rawdb.WriteCanonicalHash(tx, child.Hash(), 9); err != nil {
				return err
			}
			rawdb.WriteHeadBlockHash(tx, child.Hash())
			return rawdb.WriteHeadHeaderHash(tx, child.Hash())
		}); err != nil {
			t.Fatal(err)
		}
		bc := &BlockChain{ChainDB: db, ctx: context.Background()}
		bc.currentBlock.Store(child)
		if withIndexer {
			bc.SetTxIndexer(&testTxIndexer{})
		}
		for i := 0; i < 2; i++ { // second call proves idempotence
			if err := bc.CommitToCanonical(child.Hash()); err != nil {
				t.Fatalf("indexer=%v commit %d: %v", withIndexer, i, err)
			}
		}
		if err := db.View(context.Background(), func(tx kv.Tx) error {
			got, err := rawdb.ReadTxLookupEntry(tx, txn.Hash())
			if err != nil {
				return err
			}
			if withIndexer && got != nil {
				t.Fatalf("indexer on: tx lookup written (%d), want none", *got)
			}
			if !withIndexer && (got == nil || *got != 9) {
				t.Fatalf("indexer off: tx lookup = %v, want 9", got)
			}
			if h := rawdb.ReadHeadBlockHash(tx); h != child.Hash() {
				t.Fatalf("head = %s, want %s", h, child.Hash())
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
