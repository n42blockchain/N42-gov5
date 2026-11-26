package api_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/tracers"
	_ "github.com/n42blockchain/N42/internal/tracers/native"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func callTracerConfig() *tracers.TraceConfig {
	name := "callTracer"
	return &tracers.TraceConfig{Tracer: &name}
}

// BenchmarkExplorerTraceBlockCallTracer: debug_traceBlockByNumber with the
// callTracer (Blockscout geth variant, GETH_TRACE_BY_BLOCK) on a 1.2k-tx
// block. One iteration = one block.
func BenchmarkExplorerTraceBlockCallTracer(b *testing.B) {
	a, blocks := api.ExplorerBenchChain(b)
	t := tracers.NewAPI(a)
	nr := jsonrpc.BlockNumber(blocks[1].Number64().Uint64())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := t.TraceBlockByNumber(context.Background(), nr, callTracerConfig())
		require.NoError(b, err)
		require.Len(b, res, len(blocks[1].Transactions()))
		_, err = json.Marshal(res)
		require.NoError(b, err)
	}
}

// BenchmarkExplorerTraceLastTx: debug_traceTransaction (Blockscout's default
// geth path) for the last transaction of a 1.2k-tx block, which replays the
// whole block prefix first.
func BenchmarkExplorerTraceLastTx(b *testing.B) {
	a, blocks := api.ExplorerBenchChain(b)
	t := tracers.NewAPI(a)
	txs := blocks[1].Transactions()
	last := txs[len(txs)-1].Hash()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := t.TraceTransaction(context.Background(), last, callTracerConfig())
		require.NoError(b, err)
		_, err = json.Marshal(res)
		require.NoError(b, err)
	}
}

// TestExplorerTraceBlockMatchesTraceTx: the block trace of a transaction
// equals its single-transaction trace.
func TestExplorerTraceBlockMatchesTraceTx(t *testing.T) {
	a, blocks := api.ExplorerBenchChain(t)
	tr := tracers.NewAPI(a)
	blk := blocks[2]
	res, err := tr.TraceBlockByNumber(context.Background(), jsonrpc.BlockNumber(blk.Number64().Uint64()), callTracerConfig())
	require.NoError(t, err)
	txs := blk.Transactions()
	for _, i := range []int{0, 1, 6, len(txs) - 1} {
		one, err := tr.TraceTransaction(context.Background(), txs[i].Hash(), callTracerConfig())
		require.NoError(t, err)
		want, _ := json.Marshal(one)
		got, _ := json.Marshal(res[i])
		require.Contains(t, string(got), string(want), "tx %d", i)
		// Blockscout's block tracing matches %{"txHash" => _, "result" => _}.
		require.Contains(t, string(got), `"txHash":"`+txs[i].Hash().Hex()+`"`, "tx %d", i)
	}
}
