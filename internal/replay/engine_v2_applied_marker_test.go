// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// A QMDB replay target must carry the applied marker at its head. Genesis init
// leaves the marker at block 0; a node booting from a target whose marker is
// still 0 treats the populated PlainState as a half-healed discontinuity and
// rolls the canonical head back to genesis.
func TestEngineV2_QMDBAppliedMarkerFollowsHead(t *testing.T) {
	src := rpTBuildSourceChain(t, 2)
	dstDir := t.TempDir()

	cfg := DefaultConfigV2()
	cfg.SourcePath = src.Dir
	cfg.TargetPath = dstDir
	cfg.ChainConfig = src.Config
	cfg.ChainName = "mainnet_v2"
	cfg.TreeType = "qmdb"
	cfg.FillGaps = false
	cfg.AutoTopup = true
	cfg.FromBlock = 1
	cfg.SnapshotAtEnd = false

	e, err := NewEngineV2(cfg)
	require.NoError(t, err)
	stats, err := e.Run(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 2, stats.BlocksProcessed.Load())
	e.Close()

	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbx.Open(context.Background(), dstDir+"/chaindata", log2.New(), true)
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		head := rawdb.ReadHeadBlockHash(tx)
		num := rawdb.ReadHeaderNumber(tx, types.Hash(head))
		require.NotNil(t, num)
		n, h, ok, err := rawdb.ReadQMDBApplied(tx)
		require.NoError(t, err)
		require.True(t, ok, "applied marker missing")
		require.Equal(t, *num, n, "applied marker must sit at the replay head")
		require.Equal(t, [32]byte(head), h)
		return nil
	}))
}
