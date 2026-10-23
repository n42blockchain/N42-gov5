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
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/stretchr/testify/require"
)

func lifecycleRecSplitArgs(tmpDir string, keyCount int) RecSplitArgs {
	return RecSplitArgs{
		KeyCount:           keyCount,
		BucketSize:         10,
		Salt:               1,
		TmpDir:             tmpDir,
		IndexFile:          filepath.Join(tmpDir, "index"),
		LeafSize:           8,
		Enums:              true,
		LessFalsePositives: true,
	}
}

func lifecycleBufferFiles(t *testing.T, tmpDir string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(tmpDir, "erigon-lfp-buf-*"))
	require.NoError(t, err)
	return paths
}

func requireNoLifecycleBuffer(t *testing.T, tmpDir string) {
	t.Helper()
	require.Empty(t, lifecycleBufferFiles(t, tmpDir))
}

func addLifecycleKeys(t *testing.T, rs *RecSplit, keys []string) {
	t.Helper()
	for i, key := range keys {
		require.NoError(t, rs.AddKey([]byte(key), uint64(i)))
	}
}

func TestRecSplitLFPConstructorErrorRemovesTempBuffer(t *testing.T) {
	tmpDir := t.TempDir()
	args := lifecycleRecSplitArgs(tmpDir, 2)
	args.LeafSize = MaxLeafSize + 1

	rs, err := NewRecSplit(args, log.New())
	require.Error(t, err)
	require.Nil(t, rs)
	requireNoLifecycleBuffer(t, tmpDir)
}

func TestRecSplitLFPCloseBeforeBuildIsIdempotentAndPreservesIndexPaths(t *testing.T) {
	tmpDir := t.TempDir()
	args := lifecycleRecSplitArgs(tmpDir, 1)
	indexContents := []byte("pre-existing index sentinel")
	tmpContents := []byte("pre-existing tmp sentinel")
	require.NoError(t, os.WriteFile(args.IndexFile, indexContents, 0o600))
	require.NoError(t, os.WriteFile(args.IndexFile+".tmp", tmpContents, 0o600))

	rs, err := NewRecSplit(args, log.New())
	require.NoError(t, err)
	require.NotNil(t, rs.existenceF)
	require.FileExists(t, rs.existenceF.Name())

	rs.Close()
	require.Nil(t, rs.existenceF)
	require.Nil(t, rs.existenceW)
	requireNoLifecycleBuffer(t, tmpDir)
	rs.Close()

	got, err := os.ReadFile(args.IndexFile)
	require.NoError(t, err)
	require.Equal(t, indexContents, got)
	got, err = os.ReadFile(args.IndexFile + ".tmp")
	require.NoError(t, err)
	require.Equal(t, tmpContents, got)
}

func TestRecSplitLFPBuildClosesBufferAndIndexRemainsReadable(t *testing.T) {
	tmpDir := t.TempDir()
	args := lifecycleRecSplitArgs(tmpDir, 3)
	keys := []string{"lifecycle-key-0", "lifecycle-key-1", "lifecycle-key-2"}
	rs, err := NewRecSplit(args, log.New())
	require.NoError(t, err)
	rs.DisableFsync()
	addLifecycleKeys(t, rs, keys)
	bufferPath := rs.existenceF.Name()
	require.FileExists(t, bufferPath)

	require.NoError(t, rs.Build(context.Background()))
	require.Nil(t, rs.existenceF)
	require.Nil(t, rs.existenceW)
	require.NoFileExists(t, bufferPath)
	rs.Close()
	rs.Close()

	idx, err := OpenIndex(args.IndexFile)
	require.NoError(t, err)
	defer idx.Close()
	reader := NewIndexReader(idx)
	defer reader.Close()
	for i, key := range keys {
		enum, ok := reader.Lookup([]byte(key))
		require.True(t, ok)
		require.Equal(t, uint64(i), idx.OrdinalLookup(enum))
		hash, _ := reader.sum([]byte(key))
		require.True(t, idx.Has(hash, enum))
	}
}

func TestRecSplitLFPBuildErrorsArePreservedAndCloseRemovesBuffer(t *testing.T) {
	t.Run("key count mismatch", func(t *testing.T) {
		tmpDir := t.TempDir()
		args := lifecycleRecSplitArgs(tmpDir, 2)
		rs, err := NewRecSplit(args, log.New())
		require.NoError(t, err)
		rs.DisableFsync()
		require.NoError(t, rs.AddKey([]byte("one-key"), 0))
		bufferPath := rs.existenceF.Name()

		err = rs.Build(context.Background())
		require.EqualError(t, err, "expected keys 2, got 1")
		require.FileExists(t, bufferPath)
		rs.Close()
		require.Nil(t, rs.existenceF)
		require.Nil(t, rs.existenceW)
		require.NoFileExists(t, bufferPath)
		rs.Close()
	})

	t.Run("existence flush", func(t *testing.T) {
		tmpDir := t.TempDir()
		args := lifecycleRecSplitArgs(tmpDir, 1)
		rs, err := NewRecSplit(args, log.New())
		require.NoError(t, err)
		rs.DisableFsync()
		wantErr := errors.New("injected existence flush failure")
		rs.existenceW = bufio.NewWriter(lifecycleErrorWriter{err: wantErr})
		require.NoError(t, rs.AddKey([]byte("flush-failure-key"), 0))
		bufferPath := rs.existenceF.Name()

		err = rs.Build(context.Background())
		require.ErrorIs(t, err, wantErr)
		require.Nil(t, rs.existenceF)
		require.Nil(t, rs.existenceW)
		require.NoFileExists(t, bufferPath)
		rs.Close()
	})
}

type lifecycleErrorWriter struct {
	err error
}

func (w lifecycleErrorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestRecSplitLFPCollisionRetryKeepsBufferUntilSuccessfulBuild(t *testing.T) {
	tmpDir := t.TempDir()
	args := lifecycleRecSplitArgs(tmpDir, 3)
	keys := []string{"retry-key-0", "retry-key-1", "retry-key-2"}
	rs, err := NewRecSplit(args, log.New())
	require.NoError(t, err)
	rs.DisableFsync()
	addLifecycleKeys(t, rs, []string{"retry-duplicate", "retry-duplicate", "retry-unique"})
	bufferPath := rs.existenceF.Name()
	bufferFile := rs.existenceF

	err = rs.Build(context.Background())
	require.ErrorIs(t, err, ErrCollision)
	require.True(t, rs.Collision())

	rs.ResetNextSalt()
	require.False(t, rs.Collision())
	require.Same(t, bufferFile, rs.existenceF)
	require.NotNil(t, rs.existenceF)
	require.FileExists(t, bufferPath)
	addLifecycleKeys(t, rs, keys)

	require.NoError(t, rs.Build(context.Background()))
	require.Nil(t, rs.existenceF)
	require.Nil(t, rs.existenceW)
	require.NoFileExists(t, bufferPath)
	rs.Close()
}
