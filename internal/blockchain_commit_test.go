package internal

import (
	"context"
	"errors"
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

// TestCommitToCanonicalIndexesTransactionsAsynchronously pins the property the
// move off the consensus path must preserve: a committed transaction is
// findable by hash.
//
// The index used to be written inside the commit transaction, where one row
// per transaction -- keyed by transaction hash, so scattered across the
// B-tree -- made mdbx_txn_commit 94.6% of the commit phase and roughly 77% of
// the whole block cycle at a 480M gas ceiling. It is not consensus state:
// nothing in validation, fork choice, or execution reads it.
func TestCommitToCanonicalIndexesTransactionsAsynchronously(t *testing.T) {
	db := newRealignTestDB(t)
	parent := block.NewBlock(&block.Header{
		Number:     uint256.NewInt(4),
		Difficulty: uint256.NewInt(1),
		Root:       types.Hash{0x04},
	}, nil).(*block.Block)
	from := types.HexToAddress("0x111")
	to := types.HexToAddress("0x222")
	txn := transaction.NewTransaction(7, from, &to, uint256.NewInt(3), 21000, uint256.NewInt(1), nil)
	child := block.NewBlock(&block.Header{
		Number:     uint256.NewInt(5),
		ParentHash: parent.Hash(),
		Difficulty: uint256.NewInt(1),
		Root:       types.Hash{0x05},
	}, []*transaction.Transaction{txn}).(*block.Block)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := rawdb.WriteBlock(tx, parent); err != nil {
			return err
		}
		if err := rawdb.WriteBlock(tx, child); err != nil {
			return err
		}
		if err := rawdb.WriteCanonicalHash(tx, parent.Hash(), 4); err != nil {
			return err
		}
		rawdb.WriteHeadBlockHash(tx, parent.Hash())
		return rawdb.WriteHeadHeaderHash(tx, parent.Hash())
	}); err != nil {
		t.Fatal(err)
	}

	bc := &BlockChain{ChainDB: db, ctx: context.Background()}
	bc.currentBlock.Store(parent)
	bc.startTxIndexer()
	if err := bc.CommitToCanonical(child.Hash()); err != nil {
		t.Fatal(err)
	}
	// Stopping drains the queue; a node that exited with blocks still queued
	// would leave gaps nothing later fills, so this is the property being
	// tested as much as the indexing itself.
	bc.stopTxIndexer()

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		n, err := rawdb.ReadTxLookupEntry(tx, txn.Hash())
		if err != nil {
			return err
		}
		if n == nil {
			return errNoLookupEntry
		}
		if *n != 5 {
			t.Fatalf("lookup entry = %d, want 5", *n)
		}
		return nil
	}); err != nil {
		t.Fatalf("transaction not findable by hash after commit: %v", err)
	}
}

var errNoLookupEntry = errors.New("no lookup entry for the committed transaction")
