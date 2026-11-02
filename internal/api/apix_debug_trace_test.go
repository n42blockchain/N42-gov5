package api

// apix_debug_trace_test.go exercises debug_trace.go's transaction/block
// tracing and CreateAccessList against the real executed-chain fixture
// (apix_chain_fixture_test.go), instead of hand-built stubs.
//
// Named tracers (callTracer, prestateTracer from internal/tracers/native)
// cannot be exercised from here: that package imports internal/tracers,
// which imports internal/api, so pulling it into an internal/api test file
// is an import cycle. debug_trace.go's own TraceTransaction path does not
// consult that registry anyway (see TestDebugTraceTransactionNamedTracerRejected).

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func apiXUint256(n uint64) *uint256.Int { return uint256.NewInt(n) }

// apiXNewAPIForFixture builds a minimal *API wired to the fixture's real
// chain and database, suitable for constructing BlockChainAPI/DebugAPI/
// otterscan/eth handlers under test.
func apiXNewAPIForFixture(fx *apiXChainFixture) *API {
	return &API{
		db:          fx.DB,
		bc:          fx.Chain,
		engine:      consensus.Engine(apiXFakerEngine{}),
		chainConfig: fx.Config,
		// setDefaults (transaction_args.go) dereferences TxsPool()
		// unconditionally for the nonce default; an empty pool is enough.
		txspool: &mockEngineTxPool{},
	}
}

func TestDebugTraceTransactionNamedTracerRejected(t *testing.T) {
	// internal/tracers/native (callTracer, prestateTracer) cannot be imported
	// from internal/api tests: it pulls in internal/tracers, which imports
	// internal/api itself (import cycle). debug_trace.go's own traceTx does
	// not go through the registry at all — any non-nil config.Tracer value
	// is rejected outright — so this covers that branch exactly as it
	// behaves in production, independent of which tracers are registered.
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	debug := NewDebugAPI(api)

	tracer := "callTracer"
	_, err := debug.TraceTransaction(context.Background(), fx.CallTx.Hash(), &TraceConfig{Tracer: &tracer})
	require.EqualError(t, err, "custom tracers not yet supported")
}

func TestDebugTraceTransactionNotFound(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	debug := NewDebugAPI(api)

	_, err := debug.TraceTransaction(context.Background(), types.Hash{0xde, 0xad}, nil)
	require.Error(t, err)
}

func TestDebugTraceBlockByNumber(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	debug := NewDebugAPI(api)

	// The replay executes against the parent state, where the sender is funded.
	results, err := debug.TraceBlockByNumber(context.Background(), jsonrpc.BlockNumber(1), nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, fx.ValueTransferTxs[0].Hash(), results[0].TxHash)
	require.Empty(t, results[0].Error)
	require.NotNil(t, results[0].Result)
}

func TestDebugTraceBlockByHash(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	debug := NewDebugAPI(api)

	results, err := debug.TraceBlockByNumber(context.Background(), jsonrpc.BlockNumber(fx.ContractBlockNumber), nil)
	require.NoError(t, err)
	require.Len(t, results, 2, "contract-creation block carries the create + a value transfer")

	blk, berr := fx.Chain.GetBlockByNumber(apiXUint256(fx.ContractBlockNumber))
	require.NoError(t, berr)
	require.NotNil(t, blk)

	byHash, err := debug.TraceBlockByHash(context.Background(), blk.Hash(), nil)
	require.NoError(t, err)
	require.Len(t, byHash, 2)
	for _, r := range byHash {
		require.Empty(t, r.Error)
	}
}

func TestCreateAccessListForValueTransfer(t *testing.T) {
	fx := apiXGetChainFixture(t)
	api := apiXNewAPIForFixture(fx)
	bcAPI := NewBlockChainAPI(api)

	from := avmtypes.FromastAddress(&fx.Senders[0])
	to := avmtypes.FromastAddress(&fx.Senders[1])
	tip := (*hexutil.Big)(big.NewInt(1))
	fee := (*hexutil.Big)(big.NewInt(2))
	args := TransactionArgs{
		From: from,
		To:   to,
		// Explicit EIP-1559 fees sidestep the gas price oracle (unwired in
		// this fixture's minimal *API) entirely — London is active from
		// genesis, so CreateAccessList's default-filling never reaches the
		// SuggestTipCap call when both fee fields are already set.
		MaxPriorityFeePerGas: tip,
		MaxFeePerGas:         fee,
	}
	res, err := bcAPI.CreateAccessList(context.Background(), args, nil)
	require.NoError(t, err)
	require.NotNil(t, res)
	// The funded sender must be read as funded: state stays open during execution.
	require.Empty(t, res.Error)
	require.NotZero(t, uint64(res.GasUsed))
}
