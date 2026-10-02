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
//
// A genuine historical-state gap (see the comment above
// TestDebugTraceTransactionDefaultStructLogger) means every replay here
// fails at the EVM-execution step; the tests below cover the real code paths
// up to and including that failure, and assert the documented failure mode
// instead of pretending it succeeds.

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

// Note: a default-struct-logger TraceTransaction test (the common case) is
// deliberately NOT included here. traceTx replays a block's prefix against
// state.NewPlainState(tx, targetBlockNumber-1), which resolves historical
// account values through the AccountsHistory roaring-bitmap index plus the
// AccountChangeSet "original value" rows written by PlainStateWriter. On
// this fixture that lookup returns a nil account (no error) for the funded
// senders at their very first spend, so the replay fails with "insufficient
// funds" even though the real balances are present in current PlainState —
// confirmed with a throwaway probe: the AccountsHistory index has entries
// for every address that changed, but state.NewPlainState(tx,
// 0).ReadAccountData(sender) still returns (nil, nil) for a block-0 lookup.
// This looks like a genuine gap in the GetAsOf/FindByHistory path for an
// account whose first-ever change is the one being looked up through (i.e.
// its pre-image is the genesis allocation, not a prior change-set row), but
// pinning it down further is beyond this task's test-only scope — left as a
// follow-up rather than guessed at. TestDebugTraceBlockByHash below still
// exercises traceBlock/traceTx end-to-end and documents the same symptom
// via the per-tx Error field traceBlock captures instead of propagating.

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

	// TraceBlockByNumber/traceBlock never returns a top-level error for a
	// per-transaction replay failure — it captures it in txTraceResult.Error
	// instead (see debug_trace.go's traceBlock). This exercises exactly that
	// branch: given the historical-state gap documented above
	// TestDebugTraceTransactionDefaultStructLogger, every replay here hits
	// it, so err is always nil and the result carries one Error string.
	results, err := debug.TraceBlockByNumber(context.Background(), jsonrpc.BlockNumber(1), nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, fx.ValueTransferTxs[0].Hash(), results[0].TxHash)
	require.NotEmpty(t, results[0].Error)
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
		require.NotEmpty(t, r.Error)
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
	// Same historical-state gap as TraceTransaction (see the comment above
	// TestDebugTraceTransactionDefaultStructLogger): CreateAccessList reads
	// state via state.NewPlainState(tx, headerNumber) rather than current
	// PlainState, which hits the same GetAsOf/FindByHistory resolution and
	// (for this fixture's senders) reports a zero balance instead of the
	// real one. Execution errors surface through AccessListResult.Error,
	// not a returned error, so the simulation path up to and including that
	// failure is what's exercised and asserted here.
	require.Contains(t, res.Error, "insufficient funds")
}
