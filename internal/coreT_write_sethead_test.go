package internal

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
)

// TestCoreTParseBlockCachePrime covers the pure parse table for
// N42_BLOCK_CACHE_PRIME without depending on process environment.
func TestCoreTParseBlockCachePrime(t *testing.T) {
	cases := map[string]bool{
		"1": true, "true": true, "TRUE": true, "yes": true, "on": true,
		"": false, "0": false, "false": false, "garbage": false,
	}
	for in, want := range cases {
		require.Equal(t, want, parseBlockCachePrime(in), "input %q", in)
	}
}

// TestCoreTBlockCachePrimeEnabled just calls the memoized accessor so its
// sync.Once path executes at least once in this binary.
func TestCoreTBlockCachePrimeEnabled(t *testing.T) {
	_ = BlockCachePrimeEnabled()
}

// TestCoreTWriteBlockWithStateDirect drives BlockChain.WriteBlockWithState
// directly (bypassing InsertChain/the validator) the same way the fixture's
// builder drives execution, and asserts the block becomes canonical and its
// receipts are retrievable.
func TestCoreTWriteBlockWithStateDirect(t *testing.T) {
	f := coreTNewChainFixture(t)
	bc := f.Chain
	ctx := context.Background()

	parent := bc.CurrentBlock()
	parentHeader := parent.Header().(*block.Header)
	// Sender index 3 never sends a transaction during fixture construction,
	// so its nonce is still 0 here.
	txn := coreTSignedTransfer(t, f, 3, 1, 0)

	header := &block.Header{
		ParentHash: parent.Hash(),
		Number:     uint256.NewInt(0).Add(parentHeader.Number, uint256.NewInt(1)),
		GasLimit:   parentHeader.GasLimit,
		Time:       parentHeader.Time + 10,
		Coinbase:   parentHeader.Coinbase,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(1),
	}

	var (
		receipts block.Receipts
		usedGas  uint64
		ibs      *state.IntraBlockState
	)
	engine := coreTFakerEngine{}
	err := f.DB.View(ctx, func(tx kv.Tx) error {
		reader := state.NewPlainStateReader(tx)
		ibs = state.New(reader)
		gp := new(common.GasPool)
		gp.AddGas(header.GasLimit)
		ibs.Prepare(txn.Hash(), types.Hash{}, 0)
		receipt, _, aerr := ApplyTransaction(f.Config, nil, engine, nil, gp, ibs, state.NewNoopWriter(), header, txn, &usedGas, vm2.Config{})
		if aerr != nil {
			return aerr
		}
		receipts = append(receipts, receipt)
		return nil
	})
	require.NoError(t, err)

	header.GasUsed = usedGas
	header.Root = ibs.IntermediateRoot()
	header.ReceiptHash = hash.DeriveSha(receipts)
	header.Bloom = block.CreateBloom(receipts)

	txs := []*transaction.Transaction{txn}
	builtIface, _, nopay, ferr := engine.FinalizeAndAssemble(bc, header, ibs, txs, nil, receipts)
	require.NoError(t, ferr)
	builtBlock := builtIface.(*block.Block)

	require.NoError(t, bc.WriteBlockWithState(builtBlock, receipts, ibs, nopay))

	require.Equal(t, builtBlock.Hash(), bc.CurrentBlock().Hash())
	got, err := bc.GetBlockByHash(builtBlock.Hash())
	require.NoError(t, err)
	require.NotNil(t, got)

	storedReceipts, err := bc.GetReceipts(builtBlock.Hash())
	require.NoError(t, err)
	require.Len(t, storedReceipts, 1)
}

// TestCoreTWriteBlockWithStateRejectsWrongType exercises the type-assertion
// guard: WriteBlockWithState requires ibs to be *state.IntraBlockState.
func TestCoreTWriteBlockWithStateRejectsWrongType(t *testing.T) {
	f := coreTNewChainFixture(t)
	err := f.Chain.WriteBlockWithState(f.Blocks[0], nil, "not-an-ibs", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ibs must be")
}

// TestCoreTSetHead documents SetHead's actual effect: it only persists the
// DB's HeadHeaderHash row. It does NOT touch bc.currentBlock (the in-memory
// atomic every reader call -- CurrentBlock, InsertChain's contiguity/known-
// block checks, etc. -- actually consults) and does NOT revert PlainState.
//
// DEFECT (documented, not fixed per task instructions): SetHead is dead code
// in production (no caller anywhere outside tests) and, if called on a live
// *BlockChain, would silently fail to roll the chain back: CurrentBlock()
// keeps returning the pre-SetHead head, so a caller relying on SetHead to
// prepare the chain for a re-insert at an earlier height is misled -- only a
// process restart (which re-derives bc.currentBlock from the persisted head
// pointer at startup) would observe the new head.
func TestCoreTSetHead(t *testing.T) {
	f := coreTGetChainFixture(t)
	bc := f.Chain

	before := bc.CurrentBlock().Hash()
	require.NotEqual(t, f.Genesis.Hash(), before)

	require.NoError(t, bc.SetHead(0))

	// In-memory head is unchanged.
	require.Equal(t, before, bc.CurrentBlock().Hash(), "SetHead must not (and does not) move the in-memory head")

	// The DB row, however, was updated.
	err := f.DB.View(context.Background(), func(tx kv.Tx) error {
		gotHash := rawdb.ReadHeadHeaderHash(tx)
		require.Equal(t, f.Genesis.Hash(), gotHash, "SetHead's only effect is this DB row")
		return nil
	})
	require.NoError(t, err)
}
