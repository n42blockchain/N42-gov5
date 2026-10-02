package api

// apix_backend_forkchoice_test.go exercises api_backend.go's
// StateAtBlock/StateAtTransaction, api.go's Forkchoice*/doWeb3Call helpers
// (with no engineOverlay set, so each falls through to the base chain path)
// and eth_raw.go's GetRawTransactionByBlockNumberAndIndex, all against the
// real executed-chain fixture.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func TestAPIStateAtBlockHead(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)

	headBlk, ok := fx.Chain.CurrentBlock().(*block.Block)
	require.True(t, ok)

	roTx, err := fx.DB.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()

	statedb, err := api.StateAtBlock(context.Background(), roTx, headBlk)
	require.NoError(t, err)
	require.NotNil(t, statedb)
	require.True(t, statedb.Exist(fx.Senders[0]))

	// A nil block is rejected up front.
	_, err = api.StateAtBlock(context.Background(), roTx, nil)
	require.Error(t, err)
}

func TestAPIStateAtTransactionSecondSpendBlock(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)

	// Block 4 (the contract-creation block) is each of its two senders'
	// second spend, which routes around the historical-state-at-first-
	// change defect documented in apix_debug_trace_test.go.
	blk, err := fx.Chain.GetBlockByNumber(apiXUint256(fx.ContractBlockNumber))
	require.NoError(t, err)
	concreteBlk, ok := blk.(*block.Block)
	require.True(t, ok)
	require.Len(t, concreteBlk.Transactions(), 2)

	roTx, err := fx.DB.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()

	msg, _, statedb, err := api.StateAtTransaction(context.Background(), roTx, concreteBlk, 1)
	require.NoError(t, err)
	require.NotNil(t, msg)
	require.NotNil(t, statedb)
	require.Equal(t, *concreteBlk.Transactions()[1].To(), *msg.To())

	// Genesis has no transactions.
	_, _, _, err = api.StateAtTransaction(context.Background(), roTx, fx.Genesis, 0)
	require.Error(t, err)

	// Nil block is rejected.
	_, _, _, err = api.StateAtTransaction(context.Background(), roTx, nil, 0)
	require.Error(t, err)
}

func TestAPIForkchoiceHelpersNoOverlay(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)

	head := api.ForkchoiceHeadBlock()
	require.NotNil(t, head)
	require.Equal(t, fx.Chain.CurrentBlock().Hash(), head.Hash())

	byNum := api.ForkchoiceBlockByNumber(fx.ContractBlockNumber)
	require.NotNil(t, byNum)
	require.Equal(t, fx.ContractBlockNumber, byNum.Number64().Uint64())

	byHash := api.ForkchoiceBlockByHash(head.Hash())
	require.NotNil(t, byHash)
	require.Equal(t, head.Hash(), byHash.Hash())

	require.Nil(t, api.ForkchoiceBlockByHash(types.Hash{0xde, 0xad}))

	// ForkchoiceBlockHash runs the ethCompatibleBlockHash path (no overlay),
	// which need not equal the block's own (n42-native) Hash(); just assert
	// it resolves to something non-zero.
	hash := api.ForkchoiceBlockHash(head)
	require.NotEqual(t, types.Hash{}, hash)
	require.Equal(t, types.Hash{}, api.ForkchoiceBlockHash(nil))
}

func TestAPIDoWeb3Call(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)

	roTx, err := fx.DB.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()

	latest := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)
	out, err := api.doWeb3Call(context.Background(), roTx, fx.ContractAddr, nil, latest)
	require.NoError(t, err)
	require.Empty(t, out)
}

func TestGetRawTransactionByBlockNumberAndIndex(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	txAPI := NewTransactionAPI(api, new(AddrLocker))

	raw, err := txAPI.GetRawTransactionByBlockNumberAndIndex(context.Background(), jsonrpc.BlockNumber(fx.ContractBlockNumber), 0)
	require.NoError(t, err)
	require.NotEmpty(t, raw)

	expected, merr := fx.CreateTx.Marshal()
	require.NoError(t, merr)
	require.Equal(t, []byte(expected), []byte(raw))

	// Index past the end of the block.
	raw, err = txAPI.GetRawTransactionByBlockNumberAndIndex(context.Background(), jsonrpc.BlockNumber(fx.ContractBlockNumber), 100)
	require.NoError(t, err)
	require.Nil(t, raw)

	// Nonexistent block number.
	raw, err = txAPI.GetRawTransactionByBlockNumberAndIndex(context.Background(), jsonrpc.BlockNumber(999999), 0)
	require.NoError(t, err)
	require.Nil(t, raw)
}

