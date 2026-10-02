// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEngineV2_Run_Resume replays the same source chain into the same target
// twice: the second run must detect the checkpoint written by the first and
// report "already complete" without re-processing any blocks.
func TestEngineV2_Run_Resume(t *testing.T) {
	src := rpTBuildSourceChain(t, 3)
	dstDir := t.TempDir()

	newCfg := func() ConfigV2 {
		cfg := DefaultConfigV2()
		cfg.SourcePath = src.Dir
		cfg.TargetPath = dstDir
		cfg.ChainConfig = src.Config
		cfg.ChainName = "rpT_does_not_exist"
		cfg.FillGaps = false
		cfg.SnapshotAtEnd = false
		cfg.AutoTopup = true
		cfg.FromBlock = 1
		return cfg
	}

	e1, err := NewEngineV2(newCfg())
	require.NoError(t, err)
	stats1, err := e1.Run(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 3, stats1.BlocksProcessed.Load())
	e1.Close()

	// Second engine instance, same target: should resume past the checkpoint
	// and process zero additional blocks.
	e2, err := NewEngineV2(newCfg())
	require.NoError(t, err)
	defer e2.Close()
	stats2, err := e2.Run(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 0, stats2.BlocksProcessed.Load())
}

// TestEngineV2_Run_RangePastTip sets FromBlock beyond the source chain's tip;
// Run must report completion immediately without processing any batch.
func TestEngineV2_Run_RangePastTip(t *testing.T) {
	src := rpTBuildSourceChain(t, 2)
	dstDir := t.TempDir()

	cfg := DefaultConfigV2()
	cfg.SourcePath = src.Dir
	cfg.TargetPath = dstDir
	cfg.ChainConfig = src.Config
	cfg.ChainName = "rpT_does_not_exist"
	cfg.FillGaps = false
	cfg.SnapshotAtEnd = false
	cfg.AutoTopup = true
	cfg.FromBlock = 50 // past the 2-block source tip

	e, err := NewEngineV2(cfg)
	require.NoError(t, err)
	defer e.Close()

	stats, err := e.Run(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 0, stats.BlocksProcessed.Load())
	require.EqualValues(t, 2, stats.ToBlock) // auto-detected from source
}

// TestEngineV2_Run_SkipReasons disables AutoTopup so every transfer from an
// unfunded source sender fails with "insufficient funds"; the engine must
// record each as a skip reason and as a failed tx rather than aborting Run.
func TestEngineV2_Run_SkipReasons(t *testing.T) {
	src := rpTBuildSourceChain(t, 2)
	dstDir := t.TempDir()

	cfg := DefaultConfigV2()
	cfg.SourcePath = src.Dir
	cfg.TargetPath = dstDir
	cfg.ChainConfig = src.Config
	cfg.ChainName = "rpT_does_not_exist" // target genesis has none of src's funded accounts
	cfg.FillGaps = false
	cfg.SnapshotAtEnd = false
	cfg.AutoTopup = false
	cfg.FromBlock = 1

	e, err := NewEngineV2(cfg)
	require.NoError(t, err)
	defer e.Close()

	stats, err := e.Run(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 2, stats.BlocksProcessed.Load())
	require.Greater(t, stats.TxFailed.Load(), uint64(0))
	require.Equal(t, uint64(0), stats.TxReplayed.Load())
	cnt, ok := stats.SkipReasons["evm_error"]
	require.True(t, ok)
	require.Greater(t, cnt.Load(), uint64(0))
}
