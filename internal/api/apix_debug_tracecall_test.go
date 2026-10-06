package api

// apix_debug_tracecall_test.go exercises debug_trace.go's TraceCall and
// StorageRangeAt (plus the always-empty AccountRange/SetTrieFlushInterval
// stubs) against the real executed-chain fixture.

import (
	"context"
	"math/big"
	"testing"

	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/stretchr/testify/require"
)

func TestDebugTraceCallAtLatest(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	debug := NewDebugAPI(api)

	from := avmtypes.FromastAddress(&fx.Senders[0])
	to := avmtypes.FromastAddress(&fx.Senders[1])
	args := TransactionArgs{
		From:  from,
		To:    to,
		Value: (*hexutil.Big)(apiXBigOne()),
	}
	latest := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)

	// TraceCall resolves state via state.NewPlainState(tx, headerNumber)
	// where headerNumber is the current head's own number (5) rather than
	// head+1: the same historical-state gap documented above
	// TestDebugTraceTransactionDefaultStructLogger in
	// apix_debug_trace_test.go, which reports a zero balance for senders
	// here even though PlainState currently holds the real balance. This
	// exercises the real code path up to and including that documented
	// failure.
	_, err := debug.TraceCall(context.Background(), args, latest, nil)
	require.ErrorContains(t, err, "insufficient funds")
}

func TestDebugTraceCallCustomTracerRejected(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	debug := NewDebugAPI(api)

	from := avmtypes.FromastAddress(&fx.Senders[0])
	to := avmtypes.FromastAddress(&fx.Senders[1])
	args := TransactionArgs{From: from, To: to}
	latest := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)
	tracer := "callTracer"

	_, err := debug.TraceCall(context.Background(), args, latest, &TraceCallConfig{TraceConfig: TraceConfig{Tracer: &tracer}})
	require.EqualError(t, err, "custom tracers not yet supported")
}

func TestDebugTraceCallBlockNotFound(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	debug := NewDebugAPI(api)

	from := avmtypes.FromastAddress(&fx.Senders[0])
	to := avmtypes.FromastAddress(&fx.Senders[1])
	args := TransactionArgs{From: from, To: to}
	bnh := jsonrpc.BlockNumberOrHashWithHash(types.Hash{0xde, 0xad}, false)

	_, err := debug.TraceCall(context.Background(), args, bnh, nil)
	require.Error(t, err)
}

func TestDebugStorageRangeAtAndStubs(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	debug := NewDebugAPI(api)

	// By block-number string identifier.
	result, err := debug.StorageRangeAt(context.Background(), "1", 0, fx.ContractAddr, types.Hash{}, 10)
	require.NoError(t, err)
	require.NotNil(t, result)

	// By float64 (JSON-decoded number) identifier.
	result, err = debug.StorageRangeAt(context.Background(), float64(1), 0, fx.ContractAddr, types.Hash{}, 10)
	require.NoError(t, err)
	require.NotNil(t, result)

	// By block hash string identifier.
	blk, berr := fx.Chain.GetBlockByNumber(apiXUint256(1))
	require.NoError(t, berr)
	result, err = debug.StorageRangeAt(context.Background(), blk.Hash().Hex(), 0, fx.ContractAddr, types.Hash{}, 10)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Invalid identifier type.
	_, err = debug.StorageRangeAt(context.Background(), 42, 0, fx.ContractAddr, types.Hash{}, 10)
	require.Error(t, err)

	// Unparseable numeric string.
	_, err = debug.StorageRangeAt(context.Background(), "not-a-number", 0, fx.ContractAddr, types.Hash{}, 10)
	require.Error(t, err)

	rangeResult, err := debug.AccountRange(context.Background(), jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber), nil, 10, false, false, false)
	require.NoError(t, err)
	require.NotNil(t, rangeResult)

	require.NoError(t, debug.SetTrieFlushInterval("1s"))
}

func apiXBigOne() *big.Int { return big.NewInt(1) }
