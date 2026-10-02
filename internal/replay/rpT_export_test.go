// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEngineV2_RunPostExport replays a tiny chain, then exercises
// RunPostExport's snapshot creation, EraE export and checkpoint write.
func TestEngineV2_RunPostExport(t *testing.T) {
	src := rpTBuildSourceChain(t, 2)
	dstDir := t.TempDir()

	cfg := DefaultConfigV2()
	cfg.SourcePath = src.Dir
	cfg.TargetPath = dstDir
	cfg.ChainConfig = src.Config
	cfg.ChainName = "mainnet_v2" // a real genesis (block 0) is required for EraE export to cover [0, head]
	cfg.FillGaps = false
	cfg.AutoTopup = true
	cfg.FromBlock = 1
	cfg.SnapshotAtEnd = true
	cfg.ExportEraE = true
	cfg.EraESegmentSize = 1 // one block per segment, tiny era

	e, err := NewEngineV2(cfg)
	require.NoError(t, err)
	defer e.Close()

	stats, err := e.Run(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 2, stats.BlocksProcessed.Load())

	require.NoError(t, e.RunPostExport(context.Background()))

	checkpointPath := filepath.Join(dstDir, "checkpoint.json")
	data, rerr := os.ReadFile(checkpointPath)
	require.NoError(t, rerr)
	var cp CheckpointEntry
	require.NoError(t, json.Unmarshal(data, &cp))
	require.EqualValues(t, 2, cp.SourceHead)
	require.EqualValues(t, 2, cp.Number)
	require.NotEmpty(t, cp.Hash)

	eraDir := filepath.Join(dstDir, "era")
	entries, direrr := os.ReadDir(eraDir)
	require.NoError(t, direrr)
	require.NotEmpty(t, entries)
}

// TestEngineV2_RunPostExport_NoHead calls RunPostExport against a target DB
// that was never replayed into (no head block hash) and expects an error
// rather than a panic.
func TestEngineV2_RunPostExport_NoHead(t *testing.T) {
	src := rpTBuildSourceChain(t, 1)
	dstDir := t.TempDir()

	cfg := DefaultConfigV2()
	cfg.SourcePath = src.Dir
	cfg.TargetPath = dstDir
	cfg.ChainConfig = src.Config
	cfg.ChainName = "rpT_does_not_exist"

	e, err := NewEngineV2(cfg)
	require.NoError(t, err)
	defer e.Close()

	err = e.RunPostExport(context.Background())
	require.Error(t, err)
}
