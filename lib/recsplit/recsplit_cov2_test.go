/*
   Copyright 2026 Erigon contributors

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

package recsplit

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/stretchr/testify/require"
)

// TestMustOpenPanicsOnMissingFile covers the panic path of MustOpen and the
// error path of OpenIndex when the underlying file does not exist.
func TestMustOpenPanicsOnMissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	missing := filepath.Join(tmpDir, "does-not-exist")

	_, err := OpenIndex(missing)
	require.Error(t, err)

	defer func() {
		r := recover()
		require.NotNil(t, r, "expected MustOpen to panic on missing file")
	}()
	MustOpen(missing)
}

// TestRecSplitNoValuesMode covers the pure-MPHF (NoValues) build/lookup path,
// where Lookup returns the hash slot directly (bytesPerRec == 0 branch).
func TestRecSplitNoValuesMode(t *testing.T) {
	logger := log.New()
	tmpDir := t.TempDir()
	indexFile := filepath.Join(tmpDir, "index")
	const n = 30
	rs, err := NewRecSplit(RecSplitArgs{
		KeyCount:   n,
		BucketSize: 10,
		Salt:       0,
		TmpDir:     tmpDir,
		IndexFile:  indexFile,
		LeafSize:   8,
		NoValues:   true,
	}, logger)
	require.NoError(t, err)
	rs.DisableFsync()
	for i := 0; i < n; i++ {
		require.NoError(t, rs.AddKey([]byte(fmt.Sprintf("nv-key-%d", i)), 0))
	}
	require.NoError(t, rs.Build(context.Background()))
	rs.Close()

	idx := MustOpen(indexFile)
	defer idx.Close()
	reader := NewIndexReader(idx)
	seen := make(map[uint64]bool, n)
	for i := 0; i < n; i++ {
		slot, ok := reader.Lookup([]byte(fmt.Sprintf("nv-key-%d", i)))
		require.True(t, ok)
		require.False(t, seen[slot], "slot %d reused: not a bijection", slot)
		seen[slot] = true
	}
	require.Len(t, seen, n)
}

// TestRecSplitEnumsIncompatibleWithNoValues covers the NewRecSplit validation
// error path when both Enums and NoValues are requested together.
func TestRecSplitEnumsIncompatibleWithNoValues(t *testing.T) {
	logger := log.New()
	tmpDir := t.TempDir()
	_, err := NewRecSplit(RecSplitArgs{
		KeyCount:   2,
		BucketSize: 10,
		TmpDir:     tmpDir,
		IndexFile:  filepath.Join(tmpDir, "index"),
		LeafSize:   8,
		Enums:      true,
		NoValues:   true,
	}, logger)
	require.Error(t, err)
}

// TestRecSplitLargeMultiLevel builds a large enough key set to walk the
// secondary/primary aggregation levels inside recsplitCurrentBucket/recsplit
// and the matching multi-level branches of Index.Lookup (skipNodes, etc).
func TestRecSplitLargeMultiLevel(t *testing.T) {
	logger := log.New()
	tmpDir := t.TempDir()
	indexFile := filepath.Join(tmpDir, "index")
	const n = 4000
	rs, err := NewRecSplit(RecSplitArgs{
		KeyCount:   n,
		BucketSize: 2000, // force large buckets so multi-level split kicks in
		Salt:       0,
		TmpDir:     tmpDir,
		IndexFile:  indexFile,
		LeafSize:   8,
	}, logger)
	require.NoError(t, err)
	rs.DisableFsync()
	for i := 0; i < n; i++ {
		require.NoError(t, rs.AddKey([]byte(fmt.Sprintf("big-key-%d", i)), uint64(i)))
	}
	require.NoError(t, rs.Build(context.Background()))
	rs.Close()

	idx := MustOpen(indexFile)
	defer idx.Close()
	reader := NewIndexReader(idx)
	for i := 0; i < n; i += 37 { // sample to keep the test fast
		offset, ok := reader.Lookup([]byte(fmt.Sprintf("big-key-%d", i)))
		require.True(t, ok)
		require.Equal(t, uint64(i), offset)
	}
}
