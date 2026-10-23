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

func TestRecSplitResetNextSaltRebuildsFingerprints(t *testing.T) {
	for _, tc := range []struct {
		name       string
		inMemory   bool
		keyCount   int
		middleSize int
	}{
		{name: "etl-buffered", keyCount: 24, middleSize: 11},
		{name: "etl-spilled", keyCount: 4201, middleSize: 2050},
		{name: "in-memory-buffered", inMemory: true, keyCount: 24, middleSize: 11},
		{name: "in-memory-spilled", inMemory: true, keyCount: 4201, middleSize: 2050},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			args := lifecycleRecSplitArgs(tmpDir, tc.keyCount)
			args.IndexFile = filepath.Join(tmpDir, "reset-index")
			args.InMemory = tc.inMemory
			rs, err := NewRecSplit(args, log.New())
			require.NoError(t, err)
			t.Cleanup(rs.Close)
			rs.DisableFsync()

			collisionKeys := resetTestKeys("collision-tail", tc.keyCount)
			collisionKeys[0], collisionKeys[1] = "collision-key", "collision-key"
			addLifecycleKeys(t, rs, collisionKeys)
			err = rs.Build(context.Background())
			require.ErrorIs(t, err, ErrCollision)
			require.True(t, rs.Collision())

			rs.ResetNextSalt()
			addLifecycleKeys(t, rs, resetTestKeys("discarded-middle", tc.middleSize))
			rs.ResetNextSalt()

			keys := resetTestKeys("reset-key", tc.keyCount)
			addLifecycleKeys(t, rs, keys)
			require.NoError(t, rs.Build(context.Background()))
			require.Nil(t, rs.existenceF)
			require.Nil(t, rs.existenceW)
			rs.Close()

			idx, err := OpenIndex(args.IndexFile)
			require.NoError(t, err)
			defer idx.Close()
			reader := NewIndexReader(idx)
			defer reader.Close()
			wantFingerprints := make([]byte, len(keys))
			for i, key := range keys {
				bucketHash, _ := reader.sum([]byte(key))
				wantFingerprints[i] = byte(bucketHash)
			}
			require.Equal(t, wantFingerprints, idx.existence)
			for i, key := range keys {
				bucketHash, _ := reader.sum([]byte(key))
				enum, ok := reader.Lookup([]byte(key))
				require.True(t, ok, "retry key %q was rejected by its existence fingerprint", key)
				require.Equal(t, uint64(i), enum)
				require.Equal(t, uint64(i), idx.OrdinalLookup(enum))
				require.True(t, idx.Has(bucketHash, enum), "retry fingerprint missing for %q", key)
			}
		})
	}
}

func TestRecSplitResetNextSaltClearsGolombRiceAfterLateCollision(t *testing.T) {
	for _, inMemory := range []bool{false, true} {
		name := "etl"
		if inMemory {
			name = "in-memory"
		}
		t.Run(name, func(t *testing.T) {
			const keyCount = 24
			tmpDir := t.TempDir()
			args := lifecycleRecSplitArgs(tmpDir, keyCount)
			args.IndexFile = filepath.Join(tmpDir, "plain-reset-index")
			args.LessFalsePositives = false
			args.InMemory = inMemory
			rs, err := NewRecSplit(args, log.New())
			require.NoError(t, err)
			t.Cleanup(rs.Close)
			rs.DisableFsync()

			keys := make([]string, 0, keyCount)
			for candidate := 0; len(keys) < keyCount-2 && candidate < 100000; candidate++ {
				key := fmt.Sprintf("first-bucket-%d", candidate)
				if resetFingerprintBucket(rs, key) == 0 {
					keys = append(keys, key)
				}
			}
			require.Len(t, keys, keyCount-2, "could not find enough deterministic keys in bucket 0")
			lastBucket := rs.bucketCount - 1
			var collisionKey string
			for candidate := 0; candidate < 100000; candidate++ {
				key := fmt.Sprintf("last-bucket-%d", candidate)
				if resetFingerprintBucket(rs, key) == lastBucket {
					collisionKey = key
					break
				}
			}
			require.NotEmpty(t, collisionKey, "could not find a deterministic key in the last bucket")
			keys = append(keys, collisionKey, collisionKey)
			addLifecycleKeys(t, rs, keys)
			if err := rs.Build(context.Background()); err == nil {
				t.Fatal("expected duplicate-key collision in the last bucket")
			} else {
				require.ErrorIs(t, err, ErrCollision)
			}
			require.Greater(t, rs.gr.Bits(), 0, "bucket 0 should have appended GolombRice bits before the late collision")

			rs.ResetNextSalt()
			require.Zero(t, rs.gr.Bits())
			require.Empty(t, rs.gr.data)
			validKeys := resetTestKeys("plain-retry-key", keyCount)
			addLifecycleKeys(t, rs, validKeys)
			require.NoError(t, rs.Build(context.Background()))
			rs.Close()

			idx, err := OpenIndex(args.IndexFile)
			require.NoError(t, err)
			defer idx.Close()
			reader := NewIndexReader(idx)
			defer reader.Close()
			for i, key := range validKeys {
				enum, ok := reader.Lookup([]byte(key))
				require.True(t, ok, "retry key %q was rejected", key)
				require.Equal(t, uint64(i), enum)
				require.Equal(t, uint64(i), idx.OrdinalLookup(enum))
			}
		})
	}
}

func resetFingerprintBucket(rs *RecSplit, key string) uint64 {
	rs.hasher.Reset()
	_, _ = rs.hasher.Write([]byte(key))
	hi, _ := rs.hasher.Sum128()
	return remap(hi, rs.bucketCount)
}

func TestRecSplitResetNextSaltClosedBufferCannotBuild(t *testing.T) {
	const keyCount = 4100
	tmpDir := t.TempDir()
	args := lifecycleRecSplitArgs(tmpDir, keyCount)
	args.IndexFile = filepath.Join(tmpDir, "closed-buffer-index")
	rs, err := NewRecSplit(args, log.New())
	require.NoError(t, err)
	t.Cleanup(rs.Close)
	rs.DisableFsync()
	require.NoError(t, rs.AddKey([]byte("discard-before-reset"), 0))
	rs.ResetNextSalt()
	require.NoError(t, rs.existenceF.Close())

	var addErr error
	for i := 0; i < keyCount; i++ {
		addErr = rs.AddKey([]byte(fmt.Sprintf("closed-buffer-%d", i)), uint64(i))
		if addErr != nil {
			break
		}
	}
	require.Error(t, addErr, "a WriteAt error on the closed scratch file must reach AddKey")
	require.Error(t, rs.Build(context.Background()), "a closed scratch file must not produce a successful build")
	rs.Close()
}

func resetTestKeys(prefix string, count int) []string {
	keys := make([]string, count)
	for i := range keys {
		keys[i] = fmt.Sprintf("%s-%d", prefix, i)
	}
	return keys
}
