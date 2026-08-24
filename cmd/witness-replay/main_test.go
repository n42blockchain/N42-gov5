package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveCodeInputs(t *testing.T) {
	t.Parallel()

	t.Run("mdbx suppresses implicit freezer", func(t *testing.T) {
		hbPath := t.TempDir()
		datadir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(hbPath, "codes.cidx"), nil, 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(datadir, "mdbx.dat"), nil, 0o600))

		codesDir, hasMDBX, autoDetected, err := resolveCodeInputs(hbPath, datadir, "")
		require.NoError(t, err)
		require.Empty(t, codesDir)
		require.True(t, hasMDBX)
		require.False(t, autoDetected)
	})

	t.Run("explicit freezer keeps precedence", func(t *testing.T) {
		hbPath := t.TempDir()
		datadir := t.TempDir()
		explicit := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(hbPath, "codes.cidx"), nil, 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(datadir, "mdbx.dat"), nil, 0o600))

		codesDir, hasMDBX, autoDetected, err := resolveCodeInputs(hbPath, datadir, explicit)
		require.NoError(t, err)
		require.Equal(t, explicit, codesDir)
		require.True(t, hasMDBX)
		require.False(t, autoDetected)
	})

	t.Run("freezer is auto-detected without mdbx", func(t *testing.T) {
		hbPath := t.TempDir()
		datadir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(hbPath, "codes.cidx"), nil, 0o600))

		codesDir, hasMDBX, autoDetected, err := resolveCodeInputs(hbPath, datadir, "")
		require.NoError(t, err)
		require.Equal(t, hbPath, codesDir)
		require.False(t, hasMDBX)
		require.True(t, autoDetected)
	})
}
