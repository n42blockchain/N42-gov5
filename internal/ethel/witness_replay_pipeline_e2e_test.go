// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// witness_replay_pipeline_e2e_test.go drives RunWitnessReplay end to end
// over a tiny synthetic geth-format freezer (empty, post-merge blocks) and
// a hand-built witness input freezer, exercising feedBlocks, absorb,
// writeOne and the aggregator/writer wiring without any real datadir.

package ethel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

// buildWitnessInputFreezer writes n (one per block 0..n-1) witness entries —
// here all empty, matching the empty-body blocks from buildSyntheticGethFreezer —
// into a fresh freezer via outputBatcher, the same writer RunWitnessReplay's
// own output path uses.
func buildWitnessInputFreezer(t *testing.T, dir string, n int) {
	t.Helper()
	fz, err := freezer.New(dir, 0)
	require.NoError(t, err)
	defer fz.Close()

	b, err := newOutputBatcher(fz)
	require.NoError(t, err)
	defer b.Close()

	for i := 0; i < n; i++ {
		require.NoError(t, b.addEntry(freezer.TableBlockWitness, "c", nil))
	}
	require.NoError(t, b.flushAll())
	require.NoError(t, b.sync())
}

// ethWReplayEthConfig returns a chain config whose Byzantium activation is
// far beyond the tiny synthetic chains used here, so receipt-root
// verification (gated on IsByzantium) stays off and empty blocks need no
// real EVM state.
func ethWReplayEthConfig() *params.ChainConfig {
	return params.EthereumMainnetChainConfig
}

// TestRunWitnessReplayEndToEnd replays blocks 1..3 of a 4-block synthetic
// chain (block 0 held back for the pipeline's genesis path) through the
// sequential feedBlocks reader and the async writer, then checks the output
// freezer's acctcs/storcs/wipes tables were populated for every replayed
// block.
func TestRunWitnessReplayEndToEnd(t *testing.T) {
	hbDir := t.TempDir()
	fz := buildSyntheticGethFreezer(t, hbDir, 4)
	defer fz.Close()

	witDir := t.TempDir()
	buildWitnessInputFreezer(t, witDir, 4)

	outDir := t.TempDir()

	codeDB := memdb.NewTestDB(t)
	chainCfg := ethWReplayEthConfig()
	engine := NewEthReplayEngine(chainCfg)

	cfg := WitnessReplayConfig{
		HeadersBodiesPath: hbDir,
		WitnessPath:       witDir,
		OutputPath:        outDir,
		StartBlock:        1,
		EndBlock:          4,
		Workers:           2,
		ChainCfg:          chainCfg,
		Engine:            engine,
	}

	require.NoError(t, RunWitnessReplay(context.Background(), cfg, codeDB))

	outFz, err := freezer.New(outDir, 0)
	require.NoError(t, err)
	defer outFz.Close()

	acctTbl := outFz.Table(freezer.TableAccountChanges)
	require.NotNil(t, acctTbl)
	require.Equal(t, uint64(4), acctTbl.Items()) // 0 (padded) + 1,2,3 replayed

	stoTbl := outFz.Table(freezer.TableStorageChanges)
	require.NotNil(t, stoTbl)
	require.Equal(t, uint64(4), stoTbl.Items())

	wipesTbl := outFz.Table(freezer.TableWipes)
	require.NotNil(t, wipesTbl)
	require.Equal(t, uint64(4), wipesTbl.Items())

	// No receipts table expected: WriteReceipts defaults to false.
	require.Nil(t, outFz.Table(freezer.TableReceipts))
}

// TestRunWitnessReplayResumesFromPartialOutput runs the pipeline twice over
// the same output dir: once for blocks 1..2, then again (fresh process
// simulation) for 1..4, exercising alignOnResume's "already have N items,
// skip ahead" branch on the second call.
func TestRunWitnessReplayResumesFromPartialOutput(t *testing.T) {
	hbDir := t.TempDir()
	fz := buildSyntheticGethFreezer(t, hbDir, 4)
	defer fz.Close()

	witDir := t.TempDir()
	buildWitnessInputFreezer(t, witDir, 4)

	outDir := t.TempDir()
	codeDB := memdb.NewTestDB(t)
	chainCfg := ethWReplayEthConfig()
	engine := NewEthReplayEngine(chainCfg)

	cfg1 := WitnessReplayConfig{
		HeadersBodiesPath: hbDir,
		WitnessPath:       witDir,
		OutputPath:        outDir,
		StartBlock:        1,
		EndBlock:          2,
		Workers:           1,
		ChainCfg:          chainCfg,
		Engine:            engine,
	}
	require.NoError(t, RunWitnessReplay(context.Background(), cfg1, codeDB))

	cfg2 := cfg1
	cfg2.StartBlock = 2
	cfg2.EndBlock = 4
	require.NoError(t, RunWitnessReplay(context.Background(), cfg2, codeDB))

	outFz, err := freezer.New(outDir, 0)
	require.NoError(t, err)
	defer outFz.Close()
	require.Equal(t, uint64(4), outFz.Table(freezer.TableAccountChanges).Items())
}

// TestRunWitnessReplayRangePastTip confirms a StartBlock at or beyond the
// witness table's item count fails fast with a clear error, rather than
// silently replaying nothing.
func TestRunWitnessReplayRangePastTip(t *testing.T) {
	hbDir := t.TempDir()
	fz := buildSyntheticGethFreezer(t, hbDir, 2)
	defer fz.Close()

	witDir := t.TempDir()
	buildWitnessInputFreezer(t, witDir, 2)

	outDir := t.TempDir()
	codeDB := memdb.NewTestDB(t)
	chainCfg := ethWReplayEthConfig()
	engine := NewEthReplayEngine(chainCfg)

	cfg := WitnessReplayConfig{
		HeadersBodiesPath: hbDir,
		WitnessPath:       witDir,
		OutputPath:        outDir,
		StartBlock:        5, // past witItems=2
		EndBlock:          0,
		Workers:           1,
		ChainCfg:          chainCfg,
		Engine:            engine,
	}
	err := RunWitnessReplay(context.Background(), cfg, codeDB)
	require.Error(t, err)
}

// TestRunWitnessReplayMissingChainCfgAndEngine covers the early validation
// guards for nil ChainCfg / Engine.
func TestRunWitnessReplayMissingChainCfgAndEngine(t *testing.T) {
	codeDB := memdb.NewTestDB(t)
	cfg := WitnessReplayConfig{Workers: 1}
	err := RunWitnessReplay(context.Background(), cfg, codeDB)
	require.ErrorContains(t, err, "ChainCfg")

	cfg.ChainCfg = ethWReplayEthConfig()
	err = RunWitnessReplay(context.Background(), cfg, codeDB)
	require.ErrorContains(t, err, "Engine")
}

// TestRunWitnessReplayInvalidSegmentShard covers the segment-shard
// validation guard.
func TestRunWitnessReplayInvalidSegmentShard(t *testing.T) {
	codeDB := memdb.NewTestDB(t)
	chainCfg := ethWReplayEthConfig()
	cfg := WitnessReplayConfig{
		Workers:           1,
		ChainCfg:          chainCfg,
		Engine:            NewEthReplayEngine(chainCfg),
		SegmentShardCount: 2,
		SegmentShardIndex: 5,
	}
	err := RunWitnessReplay(context.Background(), cfg, codeDB)
	require.ErrorContains(t, err, "invalid segment shard")
}

// TestRunWitnessReplaySegmentShardRequiresNoOutput covers the guard that
// rejects a sharded run that also wants cdat output.
func TestRunWitnessReplaySegmentShardRequiresNoOutput(t *testing.T) {
	codeDB := memdb.NewTestDB(t)
	chainCfg := ethWReplayEthConfig()
	cfg := WitnessReplayConfig{
		Workers:           1,
		ChainCfg:          chainCfg,
		Engine:            NewEthReplayEngine(chainCfg),
		SegmentShardCount: 2,
		SegmentShardIndex: 0,
		NoOutput:          false,
	}
	err := RunWitnessReplay(context.Background(), cfg, codeDB)
	require.ErrorContains(t, err, "NoOutput")
}

// TestRunWitnessReplayNoCodeSourceFails covers the guard requiring at least
// one of codes-freezer or datadir(codeDB).
func TestRunWitnessReplayNoCodeSourceFails(t *testing.T) {
	hbDir := t.TempDir()
	fz := buildSyntheticGethFreezer(t, hbDir, 2)
	defer fz.Close()

	witDir := t.TempDir()
	buildWitnessInputFreezer(t, witDir, 2)

	outDir := t.TempDir()
	chainCfg := ethWReplayEthConfig()
	cfg := WitnessReplayConfig{
		HeadersBodiesPath: hbDir,
		WitnessPath:       witDir,
		OutputPath:        outDir,
		StartBlock:        1,
		EndBlock:          2,
		Workers:           1,
		ChainCfg:          chainCfg,
		Engine:            NewEthReplayEngine(chainCfg),
	}
	err := RunWitnessReplay(context.Background(), cfg, nil)
	require.ErrorContains(t, err, "codes-freezer")
}
