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

func TestSplitReplayRangesAlignedAndComplete(t *testing.T) {
	const start, end = uint64(24_100_000), uint64(24_150_000)
	ranges := splitReplayRanges(start, end, 3)
	require.Len(t, ranges, 3)
	require.Equal(t, start, ranges[0].start)
	require.Equal(t, end, ranges[len(ranges)-1].end)
	for i, r := range ranges {
		require.Less(t, r.start, r.end)
		if i > 0 {
			require.Equal(t, ranges[i-1].end, r.start)
			require.Zero(t, r.start%8192)
		}
	}
}

func TestStripShardOverrides(t *testing.T) {
	args := []string{
		"--no-output", "--start", "1", "--end=9", "--workers", "80",
		"--readers=3", "--mem-limit-gb", "24", "--process-shards=2",
		"--input-high-gb", "10", "--input-low-gb=5",
		"--segment-shard-count=2", "--segment-shard-index", "1",
		"--output", "/tmp/out",
	}
	require.Equal(t, []string{"--no-output", "--output", "/tmp/out"}, stripShardOverrides(args))
}
