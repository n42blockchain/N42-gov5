// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// executor_e2e_test.go drives Executor.Run over a small synthetic
// Geth-format freezer (headers/bodies/receipts/hashes/difficulty tables
// built in-process, not real geth ancient data) and an in-memory MDBX.
// Exercises NewExecutor, Run, executeBlock, processBlock, readHeader,
// readBody, makeBlockHashFunc, loadSenders, padSendersTable and the
// setter methods, without requiring any real geth datadir.

package ethel

import (
	"context"
	"testing"

	"github.com/golang/snappy"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/rlp"
	_ "github.com/n42blockchain/N42/modules" // register tables
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// emptyGethBody is the Snappy+RLP-encoded Geth body [txs, uncles] with
// no transactions, no uncles — matches what DecodeGethBody expects.
func emptyGethBody(t *testing.T) []byte {
	t.Helper()
	raw, err := rlp.EncodeToBytes([]interface{}{[]interface{}{}, []interface{}{}})
	require.NoError(t, err)
	return snappy.Encode(nil, raw)
}

func encodeGethHeader(t *testing.T, h *block.Header) []byte {
	t.Helper()
	raw, err := rlp.EncodeToBytes(h)
	require.NoError(t, err)
	return snappy.Encode(nil, raw)
}

// buildSyntheticGethFreezer writes n post-merge, empty-body blocks
// (0..n-1) into a fresh freezer at dir, via Freezer.Freeze so Frozen()
// is updated consistently with the per-table Items().
func buildSyntheticGethFreezer(t *testing.T, dir string, n int) *freezer.Freezer {
	t.Helper()
	fz, err := freezer.New(dir, 0)
	require.NoError(t, err)

	var headers, bodies, receipts, hashes, diffs [][]byte
	var parent types.Hash
	for i := 0; i < n; i++ {
		h := mkHeader(uint64(i), tsAnchor+uint64(i), parent, types.Hash{}, EthReceiptHash(nil))
		parent = h.Hash()
		headers = append(headers, encodeGethHeader(t, h))
		bodies = append(bodies, emptyGethBody(t))
		receipts = append(receipts, snappy.Encode(nil, mustRLP(t, []interface{}{})))
		hb := h.Hash()
		hashes = append(hashes, hb[:])
		diffs = append(diffs, []byte{0})
	}
	require.NoError(t, fz.Freeze(0, &freezer.FreezeData{
		Headers:    headers,
		Bodies:     bodies,
		Receipts:   receipts,
		Hashes:     hashes,
		Difficulty: diffs,
	}))
	return fz
}

func mustRLP(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := rlp.EncodeToBytes(v)
	require.NoError(t, err)
	return b
}

// TestExecutorRunEmptyChain drives Run() over 3 trivial empty post-merge
// blocks against an in-memory MDBX with no genesis state (nothing to
// transfer, nothing to reward post-merge) and CommitInterval=1 so every
// block crosses the periodic-commit/flush/progress-log path.
func TestExecutorRunEmptyChain(t *testing.T) {
	dir := t.TempDir()
	fz := buildSyntheticGethFreezer(t, dir, 3)
	defer fz.Close()

	db := memdb.NewTestDB(t)
	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)

	cfg := ExecutorConfig{
		StartBlock:     0,
		EndBlock:       2,
		CommitInterval: 1,
	}
	executor := NewExecutor(fz, db, chainCfg, engine, cfg, nil)

	// Exercise the trivial setters before Run — must not panic and must
	// not interfere with execution (nil freezer/store/readers are valid
	// "unconfigured" states).
	executor.SetVerifyHook(nil)
	executor.SetSenderStore(nil)
	executor.SetCompactReaders(nil, nil)
	executor.SetPrefetchCompactReaders(nil, nil)
	executor.SetCodesFreezer(nil)

	require.NoError(t, executor.Run(context.Background()))

	// Running again with StartBlock resumed past EndBlock must be a
	// cheap no-op (the "Already past target" early-return path).
	cfg2 := cfg
	executor2 := NewExecutor(fz, db, chainCfg, engine, cfg2, nil)
	require.NoError(t, executor2.Run(context.Background()))
}

// TestExecutorReadHeaderAndBody exercises readHeader/readBody and
// makeBlockHashFunc directly against the synthetic freezer, including
// the out-of-range BLOCKHASH path (n >= ref returns zero hash) and the
// cross-block BLOCKHASH lookup (reads block 0's header via the freezer
// fallback, not the cache).
func TestExecutorReadHeaderAndBody(t *testing.T) {
	dir := t.TempDir()
	fz := buildSyntheticGethFreezer(t, dir, 2)
	defer fz.Close()

	db := memdb.NewTestDB(t)
	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)
	executor := NewExecutor(fz, db, chainCfg, engine, ExecutorConfig{}, nil)

	h0, err := executor.readHeader(0)
	require.NoError(t, err)
	require.Equal(t, uint64(0), h0.Number.Uint64())

	b0, err := executor.readBody(0)
	require.NoError(t, err)
	require.Empty(t, b0.Transactions)
	require.Empty(t, b0.Uncles)

	h1, err := executor.readHeader(1)
	require.NoError(t, err)

	blockHashFn := executor.makeBlockHashFunc(h1)
	// n >= ref.Number returns the zero hash (no self/future block hash).
	require.Equal(t, types.Hash{}, blockHashFn(1))
	require.Equal(t, types.Hash{}, blockHashFn(2))
	// n < ref.Number reads block 0 from the freezer (not yet cached) and
	// caches it.
	require.Equal(t, h0.Hash(), blockHashFn(0))
	_, cached := executor.headerCache[0]
	require.True(t, cached, "makeBlockHashFunc must cache the header it read")
}

// TestExecutorLoadSendersNoSource confirms loadSenders returns nil (not
// an error) when neither senderStore nor senderTable is configured, for
// both the empty-block and would-be-tx-count cases.
func TestExecutorLoadSendersNoSource(t *testing.T) {
	db := memdb.NewTestDB(t)
	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)
	executor := NewExecutor(nil, db, chainCfg, engine, ExecutorConfig{}, nil)

	require.Nil(t, executor.loadSenders(0, 0))
	require.Nil(t, executor.loadSenders(5, 3))
	require.Zero(t, executor.senderMisses)
}

// TestExecutorPadSendersTable covers the pad-from-zero and
// already-past-startBlock short-circuit branches.
func TestExecutorPadSendersTable(t *testing.T) {
	dir := t.TempDir()
	outFz, err := freezer.New(dir, 0)
	require.NoError(t, err)
	defer outFz.Close()

	db := memdb.NewTestDB(t)
	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)
	executor := NewExecutor(nil, db, chainCfg, engine, ExecutorConfig{}, outFz)
	batcher, err := newOutputBatcher(outFz)
	require.NoError(t, err)
	defer batcher.Close()
	executor.outBatcher = batcher

	require.NoError(t, executor.padSendersTable(5))
	tbl := outFz.Table(freezer.TableSenders)
	require.NotNil(t, tbl)
	require.Equal(t, uint64(5), tbl.Items())

	// Second call: items (5) already >= startBlock (3) — no-op branch.
	require.NoError(t, executor.padSendersTable(3))
	require.Equal(t, uint64(5), tbl.Items())
}

// TestExecutorSetCacheBudget confirms SetCacheBudget rebuilds the
// PlainStateBuffer (observable via a fresh Stats() call returning zero).
func TestExecutorSetCacheBudget(t *testing.T) {
	db := memdb.NewTestDB(t)
	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)
	executor := NewExecutor(nil, db, chainCfg, engine, ExecutorConfig{}, nil)
	before := executor.stateBuf

	executor.SetCacheBudget(state.CacheBudget{AccountBytes: 1 << 20, StorageBytes: 1 << 20, CodeBytes: 1 << 20})
	require.NotSame(t, before, executor.stateBuf)
}

// TestReceiptLogsHash covers both branches: nil/no-logs returns the
// zero hash, and a receipt with logs returns a stable non-zero hash
// that changes if the log data changes.
func TestReceiptLogsHash(t *testing.T) {
	require.Equal(t, types.Hash{}, receiptLogsHash(nil))
	require.Equal(t, types.Hash{}, receiptLogsHash(&block.Receipt{}))

	addr := types.HexToAddress("0x00000000000000000000000000000000000001")
	r1 := &block.Receipt{Logs: []*block.Log{{Address: addr, Data: []byte{1, 2, 3}}}}
	r2 := &block.Receipt{Logs: []*block.Log{{Address: addr, Data: []byte{1, 2, 4}}}}
	h1 := receiptLogsHash(r1)
	h2 := receiptLogsHash(r2)
	require.NotEqual(t, types.Hash{}, h1)
	require.NotEqual(t, h1, h2)

	// Deterministic: same logs hash the same way twice.
	require.Equal(t, h1, receiptLogsHash(r1))
}
