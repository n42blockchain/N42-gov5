package api

// apix_blockscout2_test.go exercises blockscout.go's block/receipt/batch
// helpers (GetBlockTransactionCountByNumber, GetUncleCountByBlockNumber,
// GetUncleByBlockNumberAndIndex, GetBlockReceipts, BatchGetBalance,
// BatchGetCode, GetBlockscoutCompatibility) against the real executed-chain
// fixture.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func TestBlockChainAPIGetBlockTransactionCountByNumber(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)

	count, err := bcAPI.GetBlockTransactionCountByNumber(context.Background(), jsonrpc.BlockNumber(fx.ContractBlockNumber))
	require.NoError(t, err)
	require.NotNil(t, count)
	require.Equal(t, uint(2), uint(*count))

	count, err = bcAPI.GetBlockTransactionCountByNumber(context.Background(), jsonrpc.BlockNumber(999999))
	require.NoError(t, err)
	require.Nil(t, count)
}

func TestBlockChainAPIGetUncleHelpers(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)

	n, err := bcAPI.GetUncleCountByBlockNumber(context.Background(), jsonrpc.BlockNumber(fx.ContractBlockNumber))
	require.NoError(t, err)
	require.NotNil(t, n)
	require.Equal(t, uint(0), uint(*n))

	n, err = bcAPI.GetUncleCountByBlockNumber(context.Background(), jsonrpc.BlockNumber(999999))
	require.NoError(t, err)
	require.Nil(t, n)

	uncle, err := bcAPI.GetUncleByBlockNumberAndIndex(context.Background(), jsonrpc.BlockNumber(fx.ContractBlockNumber), 0)
	require.NoError(t, err)
	require.Nil(t, uncle)
}

func TestBlockChainAPIGetBlockReceiptsByNumberAndHash(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)

	byNum := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.BlockNumber(fx.ContractBlockNumber))
	receipts, err := bcAPI.GetBlockReceipts(context.Background(), byNum)
	require.NoError(t, err)
	require.Len(t, receipts, 2)

	blk, berr := fx.Chain.GetBlockByNumber(apiXUint256(fx.ContractBlockNumber))
	require.NoError(t, berr)
	byHash := jsonrpc.BlockNumberOrHashWithHash(blk.Hash(), false)
	receipts, err = bcAPI.GetBlockReceipts(context.Background(), byHash)
	require.NoError(t, err)
	require.Len(t, receipts, 2)
}

func TestBlockChainAPIBatchGetBalanceAndCode(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)

	latest := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)
	addrs := []types.Address{fx.Senders[0], fx.Senders[1], fx.ContractAddr}

	balances, err := bcAPI.BatchGetBalance(context.Background(), addrs, latest)
	require.NoError(t, err)
	require.Len(t, balances, 3)

	codes, err := bcAPI.BatchGetCode(context.Background(), addrs, latest)
	require.NoError(t, err)
	require.Len(t, codes, 3)
	require.Empty(t, codes[0])
	require.NotEmpty(t, codes[2])

	// Oversized batch rejected.
	tooMany := make([]types.Address, maxBatchAddresses+1)
	_, err = bcAPI.BatchGetBalance(context.Background(), tooMany, latest)
	require.Error(t, err)
	_, err = bcAPI.BatchGetCode(context.Background(), tooMany, latest)
	require.Error(t, err)
}

func TestBlockChainAPIGetBlockscoutCompatibility(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)

	info := bcAPI.GetBlockscoutCompatibility()
	require.NotNil(t, info)
	require.True(t, info.Compatible)
}
