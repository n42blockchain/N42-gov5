// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// witness_replay_pipeline_parallel_test.go exercises the NoOutput parallel
// feeder path (feedBlocksParallel / openParallelReplayInput / feedBlockRange)
// over both input backends: the geth-ancient synthetic freezer and a real
// N42 columnar compact store (headerc + bodyc), the latter built by actually
// running HeaderCompactStage.Run / BodyCompactStage.Run so n42CompactSource's
// header/body/takeBodyNoAhead/readBody/bodyResult path gets real bytes.

package ethel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// buildCompactHeadersBodiesDir writes n empty post-merge blocks into a
// throwaway geth-ancient freezer, then runs the real header/body compact
// stages against it, producing a genuine headerc.cidx/bodyc.cidx pair at
// outDir that openN42CompactSource will auto-detect.
func buildCompactHeadersBodiesDir(t *testing.T, n int) string {
	t.Helper()
	srcDir := t.TempDir()
	fz := buildSyntheticGethFreezer(t, srcDir, n)
	defer fz.Close()

	outDir := t.TempDir()
	require.NoError(t, NewHeaderCompactStage(fz, outDir).Run(context.Background()))
	require.NoError(t, NewBodyCompactStage(fz, outDir).Run(context.Background()))
	return outDir
}

// TestRunWitnessReplayParallelFeederGethSource drives the NoOutput +
// Workers>1 path (feedBlocksParallel) over the plain geth-ancient source.
func TestRunWitnessReplayParallelFeederGethSource(t *testing.T) {
	hbDir := t.TempDir()
	fz := buildSyntheticGethFreezer(t, hbDir, 4)
	defer fz.Close()

	witDir := t.TempDir()
	buildWitnessInputFreezer(t, witDir, 4)

	codeDB := memdb.NewTestDB(t)
	chainCfg := ethWReplayEthConfig()
	engine := NewEthReplayEngine(chainCfg)

	cfg := WitnessReplayConfig{
		HeadersBodiesPath: hbDir,
		WitnessPath:       witDir,
		StartBlock:        1,
		EndBlock:          4,
		Workers:           2,
		NoOutput:          true,
		ChainCfg:          chainCfg,
		Engine:            engine,
	}
	require.NoError(t, RunWitnessReplay(context.Background(), cfg, codeDB))
}

// TestRunWitnessReplayParallelFeederCompactSource drives the same
// feedBlocksParallel path, but over a real N42 columnar headerc/bodyc store,
// exercising n42CompactSource's header() (ParentHash backfill) and
// takeBodyNoAhead()/readBody()/bodyResult() instead of the geth adapter.
func TestRunWitnessReplayParallelFeederCompactSource(t *testing.T) {
	const n = 6
	hbDir := buildCompactHeadersBodiesDir(t, n)

	witDir := t.TempDir()
	buildWitnessInputFreezer(t, witDir, n)

	codeDB := memdb.NewTestDB(t)
	chainCfg := ethWReplayEthConfig()
	engine := NewEthReplayEngine(chainCfg)

	cfg := WitnessReplayConfig{
		HeadersBodiesPath: hbDir,
		WitnessPath:       witDir,
		StartBlock:        1,
		EndBlock:          uint64(n),
		Workers:           2,
		NoOutput:          true,
		ChainCfg:          chainCfg,
		Engine:            engine,
	}
	require.NoError(t, RunWitnessReplay(context.Background(), cfg, codeDB))
}

// TestN42CompactSourceHeaderAndBodyDirect opens the compact store directly
// (bypassing RunWitnessReplay) to exercise header/body/takeBody/
// takeBodyNoAhead/maxBlock/close as a unit, including the ParentHash
// backfill branch for block > 0.
func TestN42CompactSourceHeaderAndBodyDirect(t *testing.T) {
	const n = 3
	dir := buildCompactHeadersBodiesDir(t, n)

	src, err := openN42CompactSource(dir)
	require.NoError(t, err)
	defer src.close()

	// One compact segment always reports HeaderSegmentSize blocks, even
	// though only n were actually written (the rest read back as zero
	// blocks within that segment).
	require.Equal(t, uint64(HeaderSegmentSize), src.maxBlock())

	h0, err := src.header(0)
	require.NoError(t, err)
	require.Equal(t, uint64(0), h0.Number.Uint64())

	h1, err := src.header(1)
	require.NoError(t, err)
	// ParentHash must be backfilled from block 0's canonical hash.
	require.Equal(t, h0.Hash(), h1.ParentHash)

	b0, err := src.body(0)
	require.NoError(t, err)
	require.Empty(t, b0.Transactions)

	// takeBody / takeBodyNoAhead both consume the cache slot but must return
	// equivalent (empty) bodies.
	b1, err := src.takeBody(1)
	require.NoError(t, err)
	require.Empty(t, b1.Transactions)

	b2, err := src.takeBodyNoAhead(2)
	require.NoError(t, err)
	require.Empty(t, b2.Transactions)
}
