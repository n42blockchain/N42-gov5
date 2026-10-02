// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEngineV2_Run_Basic replays a small synthetic source chain into a fresh
// target datadir and checks the engine reaches the expected height.
func TestEngineV2_Run_Basic(t *testing.T) {
	src := rpTBuildSourceChain(t, 3)
	dstDir := t.TempDir()

	cfg := DefaultConfigV2()
	cfg.SourcePath = src.Dir
	cfg.TargetPath = dstDir
	cfg.ChainConfig = src.Config
	cfg.ChainName = "rpT_does_not_exist" // unknown name -> GenesisByChainName returns nil; no genesis alloc mismatch
	cfg.FillGaps = false
	cfg.SnapshotAtEnd = false
	cfg.AutoTopup = true // target genesis has none of the source accounts funded
	cfg.FromBlock = 1

	e, err := NewEngineV2(cfg)
	require.NoError(t, err)
	defer e.Close()

	stats, err := e.Run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, stats)
	require.EqualValues(t, 3, stats.ToBlock)
	require.GreaterOrEqual(t, stats.BlocksProcessed.Load(), uint64(3))
}
