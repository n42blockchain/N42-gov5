package api

// apix_accessors_test.go exercises BlockChainAPI's simple state accessors
// (GetBalance, GetCode, GetStorageAt, GetStorageValues, BlockNumber,
// ChainId, EarliestBlock) against the real executed-chain fixture at head.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func TestBlockChainAPIGetBalanceAtHead(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)

	latest := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)
	addr := *avmtypes.FromastAddress(&fx.Senders[0])
	balance, err := bcAPI.GetBalance(context.Background(), addr, latest)
	require.NoError(t, err)
	require.NotNil(t, balance)
	require.Equal(t, 1, balance.ToInt().Sign())
}

func TestBlockChainAPIGetCodeAtHead(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)

	latest := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)
	code, err := bcAPI.GetCode(context.Background(), *avmtypes.FromastAddress(&fx.ContractAddr), latest)
	require.NoError(t, err)
	require.NotEmpty(t, code)

	code, err = bcAPI.GetCode(context.Background(), *avmtypes.FromastAddress(&fx.Senders[0]), latest)
	require.NoError(t, err)
	require.Empty(t, code)
}

func TestBlockChainAPIGetStorageAtAndValuesAtHead(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)

	latest := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)
	val, err := bcAPI.GetStorageAt(context.Background(), fx.ContractAddr, types.Hash{}.Hex(), latest)
	require.NoError(t, err)
	require.NotNil(t, val)

	values, err := bcAPI.GetStorageValues(context.Background(), fx.ContractAddr, []string{types.Hash{}.Hex(), types.Hash{0x1}.Hex()}, nil)
	require.NoError(t, err)
	require.Len(t, values, 2)

	// Over the key limit is rejected.
	tooMany := make([]string, 1025)
	for i := range tooMany {
		tooMany[i] = types.Hash{}.Hex()
	}
	_, err = bcAPI.GetStorageValues(context.Background(), fx.ContractAddr, tooMany, nil)
	require.Error(t, err)
}

func TestBlockChainAPIBlockNumberChainIdEarliest(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)

	require.Equal(t, uint64(5), uint64(bcAPI.BlockNumber()))
	require.NotNil(t, bcAPI.ChainId())
	require.Equal(t, fx.Config.ChainID, bcAPI.ChainId().ToInt())
	require.Equal(t, uint64(0), uint64(bcAPI.EarliestBlock()))

	var nilAPI *BlockChainAPI
	require.Nil(t, nilAPI.ChainId())
}
