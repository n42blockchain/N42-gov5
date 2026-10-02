// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// executor_misc_test.go covers the remaining low-coverage Executor
// helpers that don't need a full Run(): loadSenders' freezer-table
// fallback branches, SetSenderFreezer/SetCodesFreezer, reportTimings,
// and dumpGasMismatch's empty-receipts fast path.

package ethel

import (
	"testing"
	"time"

	"github.com/golang/snappy"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	_ "github.com/n42blockchain/N42/modules" // register tables
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

// TestExecutorLoadSenders_FreezerTable covers the e.senderTable fallback
// path in loadSenders: exact count match, count mismatch (miss, nil),
// and txCount==0 (nil, no miss).
func TestExecutorLoadSenders_FreezerTable(t *testing.T) {
	dir := t.TempDir()
	senderFz, err := freezer.New(dir, 0)
	require.NoError(t, err)
	defer senderFz.Close()
	tbl, err := senderFz.EnsureTable(freezer.TableSenders, "c")
	require.NoError(t, err)

	addr1 := types.HexToAddress("0x1111111111111111111111111111111111111")
	addr2 := types.HexToAddress("0x2222222222222222222222222222222222222")
	twoSenders := append(append([]byte{}, addr1[:]...), addr2[:]...)
	require.NoError(t, tbl.Append(0, twoSenders)) // block 0: 2 senders
	require.NoError(t, tbl.Append(1, addr1[:]))   // block 1: 1 sender
	require.NoError(t, tbl.Append(2, []byte{}))   // block 2: 0 senders

	db := memdb.NewTestDB(t)
	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)
	executor := NewExecutor(nil, db, chainCfg, engine, ExecutorConfig{}, nil)
	executor.SetSenderFreezer(senderFz)

	// Exact match: 2 senders for 2 txs.
	got := executor.loadSenders(0, 2)
	require.Equal(t, []types.Address{addr1, addr2}, got)

	// Count mismatch (1 sender stored, 3 txs claimed) -> nil + miss counted.
	before := executor.senderMisses
	got2 := executor.loadSenders(1, 3)
	require.Nil(t, got2)
	require.Equal(t, before+1, executor.senderMisses)

	// txCount == 0 with a configured source -> nil, no miss increment.
	before2 := executor.senderMisses
	got3 := executor.loadSenders(2, 0)
	require.Nil(t, got3)
	require.Equal(t, before2, executor.senderMisses)

	// Block beyond the table's Items(): falls through to the "no source
	// matched" miss-counting branch when txCount > 0.
	before3 := executor.senderMisses
	got4 := executor.loadSenders(99, 1)
	require.Nil(t, got4)
	require.Equal(t, before3+1, executor.senderMisses)
}

// TestExecutorSetCodesFreezer_NilStateBuf exercises the (dead in normal
// usage, but present) stateBuf==nil branch of SetCodesFreezer by
// constructing an Executor literal directly, bypassing NewExecutor's
// invariant that stateBuf is always initialized.
func TestExecutorSetCodesFreezer_NilStateBuf(t *testing.T) {
	e := &Executor{}
	require.Nil(t, e.stateBuf)
	e.SetCodesFreezer(nil)
	require.Nil(t, e.codesFreezer)
}

// TestExecutorReportTimings drives collectTiming past its window so it
// invokes reportTimings (P50/P99 log line) with a non-trivial sample set,
// including the zero-duration guard for the Mgas/s division.
func TestExecutorReportTimings(t *testing.T) {
	db := memdb.NewTestDB(t)
	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)
	executor := NewExecutor(nil, db, chainCfg, engine, ExecutorConfig{TimingInterval: 3}, nil)

	for i := 0; i < 3; i++ {
		executor.collectTiming(uint64(i), timingSample{
			read: time.Millisecond, evm: 2 * time.Millisecond,
			commit: time.Millisecond, outputs: time.Millisecond,
			total: 5 * time.Millisecond, txCount: 1, gasUsed: 21000,
		})
	}
	// Window flushed after the 3rd sample.
	require.Empty(t, executor.timingSamples)

	// Direct call with a single zero-duration sample: Mgas/s guard must
	// not divide by zero.
	executor.reportTimings(10)
	executor.timingSamples = []timingSample{{total: 0, gasUsed: 100}}
	executor.reportTimings(11)
}

// TestExecutorDumpGasMismatch_EmptyReceipts drives dumpGasMismatch's
// fast path end to end: an empty-receipts Geth freezer entry for the
// block decodes to zero receipts, so the comparison loop is a trivial
// no-op and only the totals line logs.
func TestExecutorDumpGasMismatch_EmptyReceipts(t *testing.T) {
	dir := t.TempDir()
	fz, err := freezer.New(dir, 0)
	require.NoError(t, err)
	defer fz.Close()
	tbl, err := fz.EnsureTable(freezer.TableReceipts, "c")
	require.NoError(t, err)
	require.NoError(t, tbl.Append(0, snappy.Encode(nil, []byte{})))

	db := memdb.NewTestDB(t)
	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)
	executor := NewExecutor(fz, db, chainCfg, engine, ExecutorConfig{}, nil)

	header := mkHeader(0, tsAnchor, types.Hash{}, types.Hash{}, types.Hash{})
	body := &GethBodyResult{}
	result := &BlockResult{GasUsed: 0, Receipts: nil}

	// Must not panic; e.freezer is set and Ancient/DecodeGethReceipts
	// both succeed on the synthetic empty-receipts entry.
	executor.dumpGasMismatch(0, header, body, result)

	// nil e.freezer short-circuits immediately — the other branch.
	executor2 := NewExecutor(nil, db, chainCfg, engine, ExecutorConfig{}, nil)
	executor2.dumpGasMismatch(0, header, body, result)
}
