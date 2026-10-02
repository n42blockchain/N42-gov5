package node

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
)

func TestReadEthereumGenesisHashEmptyDataDir(t *testing.T) {
	hash, ok, err := ReadEthereumGenesisHash("")
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, types.Hash{}, hash)
}

func TestReadEthereumGenesisHashNoManifestYet(t *testing.T) {
	dir := t.TempDir()
	hash, ok, err := ReadEthereumGenesisHash(dir)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, types.Hash{}, hash)
}

func TestPersistEthereumGenesisHashEmptyInputsAreNoop(t *testing.T) {
	require.NoError(t, PersistEthereumGenesisHash("", types.Hash{1}))
	dir := t.TempDir()
	require.NoError(t, PersistEthereumGenesisHash(dir, types.Hash{}))
	_, ok, err := ReadEthereumGenesisHash(dir)
	require.NoError(t, err)
	require.False(t, ok, "a zero genesis hash must not create a manifest entry")
}

func TestPersistAndReadEthereumGenesisHashRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := types.Hash{0xde, 0xad, 0xbe, 0xef}

	require.NoError(t, PersistEthereumGenesisHash(dir, want))

	got, ok, err := ReadEthereumGenesisHash(dir)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, want, got)
}

func TestPersistEthereumGenesisHashPreservesExistingManifestFields(t *testing.T) {
	dir := t.TempDir()
	// Seed a manifest with an unrelated field first via the lower-level helper.
	require.NoError(t, writeDataDirNetworkManifest(dir, dataDirNetworkManifest{
		Version: dataDirNetworkManifestVersion,
		Chain:   "private",
	}))

	want := types.Hash{0x01, 0x02}
	require.NoError(t, PersistEthereumGenesisHash(dir, want))

	manifest, err := readDataDirNetworkManifest(dir)
	require.NoError(t, err)
	require.NotNil(t, manifest)
	require.Equal(t, "private", manifest.Chain)
	require.Equal(t, want.Hex(), manifest.EthereumGenesisHash)
}
