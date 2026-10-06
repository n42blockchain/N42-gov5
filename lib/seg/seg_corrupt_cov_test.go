/*
   Copyright 2021 Erigon contributors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package seg

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/stretchr/testify/require"
)

// TestDecompressorOpenPatternDepthTooDeep builds a minimal, hand-crafted
// compressed file whose pattern dictionary declares a depth greater than
// maxAllowedDepth, which readPatternDict must reject cleanly instead of
// panicking.
func TestDecompressorOpenPatternDepthTooDeep(t *testing.T) {
	// One pattern-dict entry: depth=60 (> maxAllowedDepth), pattern length 0.
	entry := []byte{60, 0}

	header := make([]byte, 24)
	binary.BigEndian.PutUint64(header[0:8], 0)                      // wordsCount
	binary.BigEndian.PutUint64(header[8:16], 0)                     // emptyWordsCount
	binary.BigEndian.PutUint64(header[16:24], uint64(len(entry)))   // dictSize

	buf := append(append([]byte{}, header...), entry...)
	buf = append(buf, make([]byte, 8)...) // posDictSize = 0

	fpath := filepath.Join(t.TempDir(), "depth_too_deep")
	require.NoError(t, os.WriteFile(fpath, buf, 0644))

	d, err := NewDecompressor(fpath)
	require.Error(t, err)
	require.True(t, errors.Is(err, &ErrCompressedFileCorrupted{}))
	require.Contains(t, err.Error(), "patternMaxDepth")
	require.Nil(t, d)
}

// TestDecompressorOpenTruncatedV11Header exercises the V1.1-format detection
// branch: a bogus dictSize that looks like the (newer) V1.1 header forces
// headerOffset=32, and the file must be rejected as "too small for V1.1
// format" rather than reading out of bounds.
func TestDecompressorOpenTruncatedV11Header(t *testing.T) {
	buf := make([]byte, 40) // >= compressedMinSize(32), but < 32+24=56
	binary.BigEndian.PutUint64(buf[16:24], 1<<41)                   // dictSize looks like V1.1 marker

	fpath := filepath.Join(t.TempDir(), "truncated_v11")
	require.NoError(t, os.WriteFile(fpath, buf, 0644))

	d, err := NewDecompressor(fpath)
	require.Error(t, err)
	require.True(t, errors.Is(err, &ErrCompressedFileCorrupted{}))
	require.Contains(t, err.Error(), "V1.1")
	require.Nil(t, d)
}

// TestGetterUncompressedWalk covers NextUncompressed, SkipUncompressed and
// MatchPrefixUncompressed on a file built with AddUncompressedWord, which
// are otherwise not reachable from the compressed-word tests.
func TestGetterUncompressedWalk(t *testing.T) {
	logger := log.New()
	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "compressed")
	c, err := NewCompressor(context.Background(), t.Name(), file, tmpDir, 1, 1, log.LvlDebug, logger)
	require.NoError(t, err)
	words := []string{"", "short", "a bit longer uncompressed word"}
	for _, w := range words {
		require.NoError(t, c.AddUncompressedWord([]byte(w)))
	}
	require.NoError(t, c.Compress())
	c.Close()

	d, err := NewDecompressor(file)
	require.NoError(t, err)
	defer d.Close()

	// Walk once with NextUncompressed, verifying contents.
	g := d.MakeGetter()
	for i, w := range words {
		require.True(t, g.HasNext(), "word %d", i)
		got, _ := g.NextUncompressed()
		require.Equal(t, w, string(got), "word %d", i)
	}
	require.False(t, g.HasNext())

	// Walk again using SkipUncompressed, checking reported lengths.
	g.Reset(0)
	for i, w := range words {
		require.True(t, g.HasNext(), "word %d", i)
		_, l := g.SkipUncompressed()
		require.Equal(t, len(w), l, "word %d", i)
	}
	require.False(t, g.HasNext())

	// MatchPrefixUncompressed lexicographically compares the full given
	// buffer against the whole word at the current offset (0 only on exact
	// equality; negative when the buffer is a strict prefix of the word).
	g.Reset(0)
	for i, w := range words {
		require.True(t, g.HasNext(), "word %d", i)
		require.Equal(t, 0, g.MatchPrefixUncompressed([]byte(w)), "word %d", i)
		if len(w) > 0 {
			require.Equal(t, -1, g.MatchPrefixUncompressed([]byte(w[:1])), "word %d", i)
			require.Equal(t, 1, g.MatchPrefixUncompressed([]byte("\xffzzz")), "word %d", i)
		} else {
			require.Equal(t, 0, g.MatchPrefixUncompressed(nil), "word %d", i)
		}
		g.SkipUncompressed()
	}
}

// TestCompressorContextCancelled verifies AddWord/AddUncompressedWord return
// the context error promptly once the context passed to NewCompressor has
// been cancelled.
func TestCompressorContextCancelled(t *testing.T) {
	logger := log.New()
	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "compressed")
	ctx, cancel := context.WithCancel(context.Background())
	c, err := NewCompressor(ctx, t.Name(), file, tmpDir, 1, 1, log.LvlDebug, logger)
	require.NoError(t, err)
	defer c.Close()

	cancel()
	require.ErrorIs(t, c.AddWord([]byte("x")), context.Canceled)
	require.ErrorIs(t, c.AddUncompressedWord([]byte("x")), context.Canceled)
}
