package node

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/conf"
)

func TestOpenDataDirEphemeralIsNoop(t *testing.T) {
	n := &Node{}
	require.NoError(t, n.openDataDir(&conf.Config{}))
	require.Nil(t, n.dirLock)
}

func TestOpenAndCloseDataDirLocksAndUnlocks(t *testing.T) {
	dir := t.TempDir()
	n := &Node{}
	cfg := &conf.Config{NodeCfg: conf.NodeConfig{DataDir: dir}}

	require.NoError(t, n.openDataDir(cfg))
	require.NotNil(t, n.dirLock)
	require.FileExists(t, filepath.Join(dir, "LOCK"))

	n.closeDataDir()
	require.Nil(t, n.dirLock)

	// closing twice must be a no-op, not a panic
	require.NotPanics(t, func() { n.closeDataDir() })
}

func TestOpenDataDirRejectsConcurrentUse(t *testing.T) {
	dir := t.TempDir()
	cfg := &conf.Config{NodeCfg: conf.NodeConfig{DataDir: dir}}

	first := &Node{}
	require.NoError(t, first.openDataDir(cfg))
	defer first.closeDataDir()

	second := &Node{}
	err := second.openDataDir(cfg)
	require.ErrorIs(t, err, ErrDatadirUsed)
}

func TestOpenDataDirCreatesMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "datadir")
	n := &Node{}
	cfg := &conf.Config{NodeCfg: conf.NodeConfig{DataDir: dir}}
	require.NoError(t, n.openDataDir(cfg))
	defer n.closeDataDir()
	require.DirExists(t, dir)
}

func TestObtainJWTSecretGeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "jwt.hex")
	n := &Node{config: &conf.Config{}}

	secret, err := n.obtainJWTSecret(secretPath)
	require.NoError(t, err)
	require.Len(t, secret, 32)
	require.FileExists(t, secretPath)

	// A second call must read back the same persisted secret.
	secret2, err := n.obtainJWTSecret(secretPath)
	require.NoError(t, err)
	require.Equal(t, secret, secret2)
}

func TestObtainJWTSecretUsesDefaultDataDirPath(t *testing.T) {
	dir := t.TempDir()
	n := &Node{config: &conf.Config{NodeCfg: conf.NodeConfig{DataDir: dir}}}

	secret, err := n.obtainJWTSecret("")
	require.NoError(t, err)
	require.Len(t, secret, 32)
	require.FileExists(t, filepath.Join(dir, datadirJWTKey))
}

func TestObtainJWTSecretRejectsWrongLength(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "jwt.hex")
	require.NoError(t, os.WriteFile(secretPath, []byte("0x1234"), 0600))

	n := &Node{config: &conf.Config{}}
	_, err := n.obtainJWTSecret(secretPath)
	require.Error(t, err)
}

func TestObtainJWTSecretRejectsInvalidHex(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "jwt.hex")
	require.NoError(t, os.WriteFile(secretPath, []byte("not-hex!!"), 0600))

	n := &Node{config: &conf.Config{}}
	_, err := n.obtainJWTSecret(secretPath)
	require.Error(t, err)
}
