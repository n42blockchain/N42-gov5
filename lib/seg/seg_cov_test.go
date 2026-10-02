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
	"path/filepath"
	"testing"
	"time"

	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/stretchr/testify/require"
)

// covBuildSimple builds a tiny compressed file with a couple of words and
// returns the resulting Decompressor, closing it automatically on test end.
func covBuildSimple(t *testing.T, words ...string) *Decompressor {
	t.Helper()
	logger := log.New()
	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "compressed")
	c, err := NewCompressor(context.Background(), t.Name(), file, tmpDir, 1, 1, log.LvlDebug, logger)
	require.NoError(t, err)
	c.SetTrace(false)
	c.DisableFsync()
	for _, w := range words {
		require.NoError(t, c.AddWord([]byte(w)))
	}
	require.NoError(t, c.Compress())
	c.Close()

	d, err := NewDecompressor(file)
	require.NoError(t, err)
	t.Cleanup(d.Close)
	return d
}

// TestCompressorMiscAccessors covers the small Compressor accessor methods
// that are otherwise never exercised directly (SetTrace, Workers, Count,
// DisableFsync).
func TestCompressorMiscAccessors(t *testing.T) {
	logger := log.New()
	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "compressed")
	c, err := NewCompressor(context.Background(), t.Name(), file, tmpDir, 1, 3, log.LvlDebug, logger)
	require.NoError(t, err)
	defer c.Close()

	require.Equal(t, 3, c.Workers())
	require.Equal(t, 0, c.Count())

	c.SetTrace(true)
	require.True(t, c.trace)
	c.SetTrace(false)
	require.False(t, c.trace)

	c.DisableFsync()
	require.True(t, c.noFsync)
	// fsync should be a no-op once disabled.
	require.NoError(t, c.fsync(nil))

	require.NoError(t, c.AddWord([]byte("hello")))
	require.NoError(t, c.AddWord([]byte("world")))
	require.Equal(t, 2, c.Count())

	require.NoError(t, c.Compress())

	d, err := NewDecompressor(file)
	require.NoError(t, err)
	defer d.Close()
	require.Equal(t, 2, d.Count())
}

// TestDecompressorMiscAccessors covers the small Decompressor accessor
// methods: Size, ModTime, IsOpen, FilePath, FileName, EmptyWordsCount,
// DataHandle, and the madvise helpers.
func TestDecompressorMiscAccessors(t *testing.T) {
	d := covBuildSimple(t, "alpha", "beta")

	require.True(t, d.IsOpen())
	require.Greater(t, d.Size(), int64(0))
	require.WithinDuration(t, time.Now(), d.ModTime(), time.Minute)
	require.NotEmpty(t, d.FilePath())
	require.Equal(t, "compressed", d.FileName())
	require.Equal(t, 0, d.EmptyWordsCount())
	require.NotNil(t, d.DataHandle())

	// madvise helpers should not panic and should be chainable.
	require.Same(t, d, d.EnableMadvNormal())
	require.Same(t, d, d.EnableMadvWillNeed())
	require.Same(t, d, d.EnableReadAhead())
	d.DisableReadAhead()

	called := false
	require.NoError(t, d.WithReadAhead(func() error {
		called = true
		return nil
	}))
	require.True(t, called)
}

// TestDecompressorMiscAccessorsOnClosed verifies the nil/closed guards on the
// madvise helpers and WithReadAhead after Close has been called.
func TestDecompressorMiscAccessorsOnClosed(t *testing.T) {
	d := covBuildSimple(t, "alpha")
	d.Close()
	require.False(t, d.IsOpen())

	// Should not panic when mmapHandle1 is nil.
	d.DisableReadAhead()
	require.Same(t, d, d.EnableReadAhead())
	require.Same(t, d, d.EnableMadvNormal())
	require.Same(t, d, d.EnableMadvWillNeed())
	require.NoError(t, d.WithReadAhead(func() error { return nil }))

	// Double close should be safe.
	d.Close()
}

// TestCompressionRatioString covers CompressionRatio.String.
func TestCompressionRatioString(t *testing.T) {
	r := CompressionRatio(2.345)
	require.Equal(t, "2.35", r.String())
}

// TestErrCompressedFileCorrupted covers Error() and Is() on the sentinel
// error type used for corruption reporting.
func TestErrCompressedFileCorrupted(t *testing.T) {
	err := &ErrCompressedFileCorrupted{FileName: "foo.seg", Reason: "bad header"}
	require.Contains(t, err.Error(), "foo.seg")
	require.Contains(t, err.Error(), "bad header")

	var target *ErrCompressedFileCorrupted
	require.True(t, err.Is(target))
}

// TestSetDecompressionTableCondensity covers the package-level setter for
// the condensity threshold, restoring the previous value afterward so other
// tests are not affected.
func TestSetDecompressionTableCondensity(t *testing.T) {
	prev := condensePatternTableBitThreshold
	defer SetDecompressionTableCondensity(prev)

	SetDecompressionTableCondensity(7)
	require.Equal(t, 7, condensePatternTableBitThreshold)
}

// TestGetterTraceAndFileName covers Getter.Trace and Getter.FileName.
func TestGetterTraceAndFileName(t *testing.T) {
	d := covBuildSimple(t, "hello")
	g := d.MakeGetter()
	g.Trace(true)
	require.True(t, g.trace)
	g.Trace(false)
	require.False(t, g.trace)
	require.Equal(t, "compressed", g.FileName())
}

// TestRawWordsFileOpenAndForEach covers OpenRawWordsFile, which is otherwise
// untested, round-tripping both compressed and uncompressed records.
func TestRawWordsFileOpenAndForEach(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "raw.idt")

	wf, err := NewRawWordsFile(path)
	require.NoError(t, err)
	require.NoError(t, wf.Append([]byte("compressed-word")))
	require.NoError(t, wf.AppendUncompressed([]byte("uncompressed-word")))
	require.NoError(t, wf.Append(nil))
	wf.Close()

	rf, err := OpenRawWordsFile(path)
	require.NoError(t, err)
	defer rf.CloseAndRemove()

	type rec struct {
		val        string
		compressed bool
	}
	var got []rec
	require.NoError(t, rf.ForEach(func(v []byte, compressed bool) error {
		got = append(got, rec{val: string(v), compressed: compressed})
		return nil
	}))

	require.Equal(t, []rec{
		{"compressed-word", true},
		{"uncompressed-word", false},
		{"", true},
	}, got)
}
