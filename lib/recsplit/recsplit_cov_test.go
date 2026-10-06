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
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/stretchr/testify/require"
)

// covBuildBasicIndex builds a small index file with given key count and
// returns its path plus the built RecSplit (already closed caller-side not needed).
func covBuildBasicIndex(t *testing.T, keyCount int, enums, lessFalsePositives bool) string {
	t.Helper()
	logger := log.New()
	tmpDir := t.TempDir()
	indexFile := filepath.Join(tmpDir, "index")
	rs, err := NewRecSplit(RecSplitArgs{
		KeyCount:           keyCount,
		BucketSize:         10,
		Salt:               0,
		TmpDir:             tmpDir,
		IndexFile:          indexFile,
		LeafSize:           8,
		Enums:              enums,
		LessFalsePositives: lessFalsePositives,
	}, logger)
	require.NoError(t, err)
	rs.DisableFsync()
	rs.LogLvl(log.LvlDebug)
	rs.SetTrace(false)
	for i := 0; i < keyCount; i++ {
		require.NoError(t, rs.AddKey([]byte(fmt.Sprintf("cov-key-%d", i)), uint64(i*11)))
	}
	require.False(t, rs.Collision())
	require.NoError(t, rs.Build(context.Background()))
	grLen, efLen := rs.Stats()
	require.GreaterOrEqual(t, grLen, 0)
	require.GreaterOrEqual(t, efLen, 0)
	rs.Close()
	return indexFile
}

// TestRecSplitSingleAndTwoKeys covers very small key counts (edge sizes 1-2)
// and the RecSplit-level accessors (Stats, Collision, DisableFsync, LogLvl, SetTrace).
func TestRecSplitSingleAndTwoKeys(t *testing.T) {
	for _, kc := range []int{1, 2} {
		kc := kc
		t.Run(fmt.Sprintf("keys=%d", kc), func(t *testing.T) {
			indexFile := covBuildBasicIndex(t, kc, false, false)
			idx := MustOpen(indexFile)
			defer idx.Close()
			require.False(t, idx.Empty())
			require.Equal(t, uint64(kc), idx.KeyCount())
			reader := NewIndexReader(idx)
			for i := 0; i < kc; i++ {
				offset, ok := reader.Lookup([]byte(fmt.Sprintf("cov-key-%d", i)))
				require.True(t, ok)
				require.Equal(t, uint64(i*11), offset)
			}
		})
	}
}

// TestRecSplitLargerSet exercises a bigger key count to walk the
// multi-level aggregation branches in recsplit() and Index.Lookup().
func TestRecSplitLargerSet(t *testing.T) {
	const n = 500
	indexFile := covBuildBasicIndex(t, n, true, true)
	idx := MustOpen(indexFile)
	defer idx.Close()
	require.True(t, idx.Enums())
	require.Equal(t, uint64(n), idx.KeyCount())
	require.False(t, idx.Empty())

	reader := NewIndexReader(idx)
	defer reader.Close()
	for i := 0; i < n; i++ {
		key := []byte(fmt.Sprintf("cov-key-%d", i))
		e, ok := reader.Lookup(key)
		require.True(t, ok)
		require.Equal(t, uint64(i), e)
		offset := idx.OrdinalLookup(e)
		require.Equal(t, uint64(i*11), offset)
		require.True(t, idx.Has(murmurBucketHashForTest(idx, key), e))
	}
}

// murmurBucketHashForTest recomputes the bucket hash the same way IndexReader does,
// to exercise Index.Has() with a real bucketHash value.
func murmurBucketHashForTest(idx *Index, key []byte) uint64 {
	r := NewIndexReader(idx)
	h, _ := r.sum(key)
	return h
}

// TestIndexAccessorsAndReaderPool covers simple field accessors on Index and
// the IndexReader pool plumbing (GetReaderFromPool, Lookup2, Empty, Close).
func TestIndexAccessorsAndReaderPool(t *testing.T) {
	indexFile := covBuildBasicIndex(t, 10, false, false)
	idx := MustOpen(indexFile)
	defer idx.Close()

	require.True(t, idx.IsOpen())
	require.Greater(t, idx.Size(), int64(0))
	require.False(t, idx.ModTime().IsZero())
	require.Equal(t, uint64(0), idx.BaseDataID())
	_, fName := filepath.Split(indexFile)
	require.Equal(t, fName, idx.FileName())
	require.Equal(t, indexFile, idx.FilePath())
	require.NotNil(t, idx.DataHandle())

	// EnableMadvNormal / EnableWillNeed should not panic and return without error.
	idx.EnableMadvNormal()
	idx.EnableWillNeed()
	idx.EnableReadAhead().DisableReadAhead()

	reader := idx.GetReaderFromPool()
	require.False(t, reader.Empty())
	offset, ok := reader.Lookup2([]byte("cov-key-"), []byte("0"))
	require.True(t, ok)
	_ = offset
	reader.Close()

	// A nil-index-backed reader Close should be a no-op.
	var nilReader *IndexReader
	nilReader.Close()
}

// TestOpenIndexFromBytes covers the in-memory (no-mmap) index open path and
// ensures lookups behave the same as the mmap'd version.
func TestOpenIndexFromBytes(t *testing.T) {
	indexFile := covBuildBasicIndex(t, 50, false, false)
	data, err := os.ReadFile(indexFile)
	require.NoError(t, err)

	idx, err := OpenIndexFromBytes(data, "mem-index")
	require.NoError(t, err)
	require.Equal(t, "mem-index", idx.FileName())
	require.Equal(t, uint64(50), idx.KeyCount())

	reader := NewIndexReader(idx)
	for i := 0; i < 50; i++ {
		offset, ok := reader.Lookup([]byte(fmt.Sprintf("cov-key-%d", i)))
		require.True(t, ok)
		require.Equal(t, uint64(i*11), offset)
	}
}

// TestRecSplitAddOffsetAndResetNextSalt covers AddOffset (enums mode) and
// ResetNextSalt, which rebuilds internal collectors/state after a collision
// or a deliberate retry.
func TestRecSplitAddOffsetAndResetNextSalt(t *testing.T) {
	logger := log.New()
	tmpDir := t.TempDir()
	indexFile := filepath.Join(tmpDir, "index")
	rs, err := NewRecSplit(RecSplitArgs{
		KeyCount:   5,
		BucketSize: 10,
		Salt:       0,
		TmpDir:     tmpDir,
		IndexFile:  indexFile,
		LeafSize:   8,
		Enums:      true,
	}, logger)
	require.NoError(t, err)
	rs.DisableFsync()

	for i := 0; i < 5; i++ {
		require.NoError(t, rs.AddKey([]byte(fmt.Sprintf("salt-key-%d", i)), uint64(i*7)))
	}
	saltBefore := rs.salt
	rs.ResetNextSalt()
	require.Equal(t, saltBefore+1, rs.salt)
	require.False(t, rs.built)
	require.False(t, rs.Collision())
	require.Equal(t, uint64(0), rs.keysAdded)

	// Re-add keys after reset and build successfully.
	for i := 0; i < 5; i++ {
		require.NoError(t, rs.AddKey([]byte(fmt.Sprintf("salt-key-%d", i)), uint64(i*7)))
	}
	require.NoError(t, rs.Build(context.Background()))
	rs.Close()

	idx := MustOpen(indexFile)
	defer idx.Close()
	reader := NewIndexReader(idx)
	for i := 0; i < 5; i++ {
		e, ok := reader.Lookup([]byte(fmt.Sprintf("salt-key-%d", i)))
		require.True(t, ok)
		offset := idx.OrdinalLookup(e)
		require.Equal(t, uint64(i*7), offset)
	}
}
