package node

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/conf"
)

func TestGetKeyStoreDirEphemeralWhenUnset(t *testing.T) {
	dir, ephemeral, err := getKeyStoreDir(&conf.NodeConfig{})
	require.NoError(t, err)
	require.True(t, ephemeral)
	require.DirExists(t, dir)
	defer os.RemoveAll(dir)
}

func TestGetKeyStoreDirDerivedFromDataDir(t *testing.T) {
	base := t.TempDir()
	dir, ephemeral, err := getKeyStoreDir(&conf.NodeConfig{DataDir: base})
	require.NoError(t, err)
	require.False(t, ephemeral)
	require.Equal(t, filepath.Join(base, "keystore"), dir)
	require.DirExists(t, dir)
}

func TestGetKeyStoreDirExplicitAbsolutePath(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "mykeys")
	dir, ephemeral, err := getKeyStoreDir(&conf.NodeConfig{KeyStoreDir: explicit})
	require.NoError(t, err)
	require.False(t, ephemeral)
	require.Equal(t, explicit, dir)
	require.DirExists(t, dir)
}
