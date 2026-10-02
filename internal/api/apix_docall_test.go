package api

// apix_docall_test.go exercises api.go's DoCall/Call/DoEstimateGas/
// EstimateGas and MinedBlock against the real executed-chain fixture, using
// "latest" so state reads go through current PlainState rather than the
// historical GetAsOf path (see the doc comment above
// TestDebugTraceTransactionDefaultStructLogger in apix_debug_trace_test.go
// for why a historical lookup is avoided here).

import (
	"context"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func apiXLatest() jsonrpc.BlockNumberOrHash {
	return jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)
}

func TestDoCallValueTransferAtHead(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)

	from := avmtypes.FromastAddress(&fx.Senders[0])
	to := avmtypes.FromastAddress(&fx.Senders[1])
	args := TransactionArgs{
		From:  from,
		To:    to,
		Value: (*hexutil.Big)(big.NewInt(1)),
	}
	result, err := DoCall(context.Background(), api, args, apiXLatest(), nil, 0, rpcGasCap)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Failed())
}

func TestDoCallContractAtHead(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)

	from := avmtypes.FromastAddress(&fx.Senders[0])
	to := avmtypes.FromastAddress(&fx.ContractAddr)
	args := TransactionArgs{
		From: from,
		To:   to,
	}
	result, err := DoCall(context.Background(), api, args, apiXLatest(), nil, 0, rpcGasCap)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Failed())
}

func TestDoCallOversizedCalldataRejected(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)

	huge := make(hexutil.Bytes, maxCallDataSize+1)
	from := avmtypes.FromastAddress(&fx.Senders[0])
	to := avmtypes.FromastAddress(&fx.Senders[1])
	args := TransactionArgs{From: from, To: to, Data: &huge}
	_, err := DoCall(context.Background(), api, args, apiXLatest(), nil, 0, rpcGasCap)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds maximum allowed")
}

func TestDoCallBlockNotFound(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)

	from := avmtypes.FromastAddress(&fx.Senders[0])
	to := avmtypes.FromastAddress(&fx.Senders[1])
	args := TransactionArgs{From: from, To: to}
	bnh := jsonrpc.BlockNumberOrHashWithHash(types.Hash{0xde, 0xad}, false)
	_, err := DoCall(context.Background(), api, args, bnh, nil, 0, rpcGasCap)
	require.Error(t, err)
}

func TestBlockChainAPICallAtHead(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)

	from := avmtypes.FromastAddress(&fx.Senders[0])
	to := avmtypes.FromastAddress(&fx.Senders[1])
	args := TransactionArgs{From: from, To: to}
	out, err := bcAPI.Call(context.Background(), args, apiXLatest(), nil)
	require.NoError(t, err)
	require.Empty(t, out)
}

func TestDoEstimateGasValueTransfer(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)

	from := avmtypes.FromastAddress(&fx.Senders[0])
	to := avmtypes.FromastAddress(&fx.Senders[1])
	args := TransactionArgs{
		From:  from,
		To:    to,
		Value: (*hexutil.Big)(big.NewInt(1)),
	}
	gas, err := DoEstimateGas(context.Background(), api, args, apiXLatest(), rpcGasCap)
	require.NoError(t, err)
	require.GreaterOrEqual(t, uint64(gas), uint64(21000))
}

func TestBlockChainAPIEstimateGasDefaultsToPending(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)

	from := avmtypes.FromastAddress(&fx.Senders[0])
	to := avmtypes.FromastAddress(&fx.Senders[1])
	args := TransactionArgs{
		From:  from,
		To:    to,
		Value: (*hexutil.Big)(big.NewInt(1)),
	}
	gas, err := bcAPI.EstimateGas(context.Background(), args, nil)
	require.NoError(t, err)
	require.GreaterOrEqual(t, uint64(gas), uint64(21000))
}

func TestMinedBlockNotificationsUnsupported(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)

	_, err := bcAPI.MinedBlock(context.Background(), fx.Senders[0])
	require.Equal(t, jsonrpc.ErrNotificationsUnsupported, err)
}
