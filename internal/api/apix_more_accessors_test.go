package api

// apix_more_accessors_test.go rounds out api.go/api_backend.go coverage:
// SubmitSign's unauthed-address path, BlockChainAPI.BlockNumber/HeaderByNumber
// resolving "latest"/"pending" tags, and api_backend's GetEVM/GetTd against
// the real executed-chain fixture.

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
)

func TestBlockChainAPISubmitSignUnauthedAddress(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)

	err := bcAPI.SubmitSign(AggSign{Address: fx.Senders[0]})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unauthed address")
}

func TestBlockChainAPIBlockNumberMatchesHead(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)

	require.Equal(t, fx.Chain.CurrentBlock().Number64().Uint64(), uint64(bcAPI.BlockNumber()))
}

func TestAPIHeaderByNumberLatestAndPending(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)

	header, err := api.HeaderByNumber(context.Background(), jsonrpc.LatestBlockNumber)
	require.NoError(t, err)
	require.NotNil(t, header)
	require.Equal(t, fx.Chain.CurrentBlock().Hash(), header.Hash())

	header, err = api.HeaderByNumber(context.Background(), jsonrpc.PendingBlockNumber)
	require.NoError(t, err)
	require.NotNil(t, header)

	header, err = api.HeaderByNumber(context.Background(), jsonrpc.BlockNumber(fx.ContractBlockNumber))
	require.NoError(t, err)
	require.NotNil(t, header)
	require.Equal(t, fx.ContractBlockNumber, header.Number64().Uint64())
}

func TestAPIBlockByNumberLatestAndPending(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)

	blk, err := api.BlockByNumber(context.Background(), jsonrpc.LatestBlockNumber)
	require.NoError(t, err)
	require.NotNil(t, blk)
	require.Equal(t, fx.Chain.CurrentBlock().Hash(), blk.Hash())

	blk, err = api.BlockByNumber(context.Background(), jsonrpc.PendingBlockNumber)
	require.NoError(t, err)
	require.NotNil(t, blk)
}

func TestAPIGetEVMAndGetTd(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)

	roTx, err := fx.DB.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()

	reader := state.NewPlainStateReader(roTx)
	ibs := state.New(reader)

	head := api.CurrentBlock()
	require.NotNil(t, head)

	msg := transaction.NewMessage(fx.Senders[0], &fx.Senders[1], 0,
		uint256.NewInt(0), 21000, uint256.NewInt(0), uint256.NewInt(0), uint256.NewInt(0),
		nil, nil, nil, nil, false, true)
	evm, vmErrFn, err := api.GetEVM(context.Background(), &msg, ibs, head, &vm.Config{})
	require.NoError(t, err)
	require.NotNil(t, evm)
	require.NotNil(t, vmErrFn)

	td := api.GetTd(context.Background(), head.Hash())
	require.NotNil(t, td)

	require.Nil(t, api.GetTd(context.Background(), types.Hash{0xde, 0xad}))
}
