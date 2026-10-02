package node

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/lib/common/datadir"
)

func TestApplyStorageTierDisabledIsNoop(t *testing.T) {
	cfg := &conf.StorageTierCfg{Enabled: false}
	dirs := &datadir.Dirs{}
	require.NoError(t, applyStorageTier(cfg, dirs))
	require.Empty(t, dirs.Chaindata)
}

func TestApplyStorageTierEnabledWithoutPathsErrors(t *testing.T) {
	cfg := &conf.StorageTierCfg{Enabled: true}
	dirs := &datadir.Dirs{}
	err := applyStorageTier(cfg, dirs)
	require.Error(t, err)
}

func TestApplyStorageTierEnabledAppliesOverrides(t *testing.T) {
	base := t.TempDir()
	hot := filepath.Join(base, "hot")
	cfg := &conf.StorageTierCfg{Enabled: true, HotPath: hot}
	dirs := datadir.New(filepath.Join(base, "datadir"))

	require.NoError(t, applyStorageTier(cfg, &dirs))
	require.DirExists(t, hot)
}
