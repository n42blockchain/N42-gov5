// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Database StateWriter that emits changesets and history indexes.
// writeIndex updates the bitmap-based history index buckets via
// bitmapdb.Get64, merging per-block changes into a roaring64 index.

package state

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"sort"

	"github.com/RoaringBitmap/roaring/roaring64"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/changeset"
	"github.com/n42blockchain/N42/modules/ethdb/bitmapdb"
)

func writeIndexBeforeCursor(blocknum uint64, changes *changeset.ChangeSet, bucket string, changeDb kv.RwTx) error {
	if changes.Len() == 0 {
		return nil
	}
	// GetAccountChanges/GetStorageChanges materialize maps afresh, so the
	// sorting done by WriteChangeSets does not carry over here. Visit the
	// history B-tree in key order and reuse a cursor for this whole block.
	sort.Sort(changes)
	cursor, err := changeDb.Cursor(bucket)
	if err != nil {
		return err
	}
	defer cursor.Close()
	seekKey := make([]byte, changes.KeySize()+8)
	binary.BigEndian.PutUint64(seekKey[changes.KeySize():], math.MaxUint32)
	var singleton []byte
	buf := bytes.NewBuffer(nil)
	// Accounts touched together commonly have identical history tails. Their
	// merge with this block number is identical too. Keep a bounded, per-call
	// memo of small tails, avoiding repeated roaring decode/normalize/encode
	// work without caching any database lookup or changing chunk selection.
	const maxMergedTails = 128
	mergedTails := make(map[string][]byte)
	for _, change := range changes.Changes {
		k := change.Key
		copy(seekKey, k)
		found, value, err := cursor.Seek(seekKey)
		if err != nil {
			return fmt.Errorf("find chunk failed: %w", err)
		}
		// Preserve Get64(from=to=MaxUint32)'s single-chunk selection. A new
		// key has the same singleton bitmap as every other new key this block;
		// encode it once instead of allocating a roaring tree per recipient.
		if !bytes.HasPrefix(found, k) {
			if singleton == nil {
				index := roaring64.BitmapOf(blocknum)
				index.RunOptimize()
				singleton, err = index.ToBytes()
				if err != nil {
					return err
				}
			}
			tailKey := make([]byte, len(k)+8)
			copy(tailKey, k)
			binary.BigEndian.PutUint64(tailKey[len(k):], math.MaxUint64)
			if err := changeDb.Put(bucket, tailKey, singleton); err != nil {
				return err
			}
			continue
		}
		if merged, ok := mergedTails[string(value)]; ok {
			tailKey := make([]byte, len(k)+8)
			copy(tailKey, k)
			binary.BigEndian.PutUint64(tailKey[len(k):], math.MaxUint64)
			if err := changeDb.Put(bucket, tailKey, merged); err != nil {
				return err
			}
			continue
		}
		index := roaring64.New()
		if _, err := index.ReadFrom(bytes.NewReader(value)); err != nil {
			return fmt.Errorf("read chunk failed: %w", err)
		}
		index.Add(blocknum)
		if index.GetSerializedSizeInBytes() <= bitmapdb.ChunkLimit {
			// This is already one complete tail chunk. The general splitter
			// constructs a range bitmap and intersects it with a copy just to
			// return these same entries. Normalize and encode the owned bitmap
			// directly; use the pre-normalization size like CutLeft64 does.
			index.RunOptimize()
			buf.Reset()
			if _, err := index.WriteTo(buf); err != nil {
				return err
			}
			tailKey := make([]byte, len(k)+8)
			copy(tailKey, k)
			binary.BigEndian.PutUint64(tailKey[len(k):], math.MaxUint64)
			encoded := types.CopyBytes(buf.Bytes())
			if len(mergedTails) < maxMergedTails && uint64(len(value)) <= bitmapdb.ChunkLimit {
				// Copy the original MDBX bytes before Put can invalidate them.
				mergedTails[string(value)] = encoded
			}
			if err := changeDb.Put(bucket, tailKey, encoded); err != nil {
				return err
			}
			continue
		}
		if err = bitmapdb.WalkChunkWithKeys64(k, index, bitmapdb.ChunkLimit, func(chunkKey []byte, chunk *roaring64.Bitmap) error {
			buf.Reset()
			if _, err = chunk.WriteTo(buf); err != nil {
				return err
			}
			return changeDb.Put(bucket, chunkKey, types.CopyBytes(buf.Bytes()))
		}); err != nil {
			return err
		}
	}

	return nil
}
