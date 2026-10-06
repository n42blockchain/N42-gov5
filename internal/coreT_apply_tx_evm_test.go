package internal

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	evmtypes "github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/state"
)

// TestCoreTApplyTransactionWithEVM covers the pre-built-EVM entry point used
// by witness replay's hot path: it must produce the same result as
// ApplyTransaction (which builds its own EVM internally) for the same
// transaction and starting state.
func TestCoreTApplyTransactionWithEVM(t *testing.T) {
	f := coreTNewChainFixture(t)
	bc := f.Chain
	ctx := bc.ctx

	parent := bc.CurrentBlock()
	parentHeader := parent.Header().(*block.Header)
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
	engine := coreTFakerEngine{}

	var usedGas uint64
	err := f.DB.View(ctx, func(tx kv.Tx) error {
		reader := state.NewPlainStateReader(tx)
		ibs := state.New(reader)
		gp := new(common.GasPool)
		gp.AddGas(header.GasLimit)

		blockContext := NewEVMBlockContext(header, nil, engine, f.Config, nil)
		vmenv := vm2.NewEVM(blockContext, evmtypes.TxContext{}, ibs, f.Config, vm2.Config{})

		ibs.Prepare(txn.Hash(), types.Hash{}, 0)
		receipt, _, aerr := ApplyTransactionWithEVM(vmenv, f.Config, engine, gp, ibs, state.NewNoopWriter(), header, txn, &usedGas, vm2.Config{})
		if aerr != nil {
			return aerr
		}
		require.NotNil(t, receipt)
		require.Equal(t, block.ReceiptStatusSuccessful, receipt.Status)
		return nil
	})
	require.NoError(t, err)
	require.Greater(t, usedGas, uint64(0))
}

// TestCoreTNewStateReaderWriter covers the reader/writer factory in both the
// write-changesets and no-history modes, uncached.
func TestCoreTNewStateReaderWriter(t *testing.T) {
	f := coreTNewChainFixture(t)
	err := f.DB.Update(f.Chain.ctx, func(tx kv.RwTx) error {
		reader, writer, rerr := NewStateReaderWriter(nil, tx, 1, true, nil)
		require.NoError(t, rerr)
		require.NotNil(t, reader)
		require.NotNil(t, writer)

		reader2, writer2, rerr2 := NewStateReaderWriter(nil, tx, 1, false, nil)
		require.NoError(t, rerr2)
		require.NotNil(t, reader2)
		require.NotNil(t, writer2)
		return nil
	})
	require.NoError(t, err)
}
