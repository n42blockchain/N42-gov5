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
// originalAccountData produces the pre-image bytes for account diffs,
// optionally zeroing CodeHash/Root when omitHashes is set for
// hash-insensitive replay.
// writeIndex updates the bitmap-based history index buckets via
// bitmapdb.Get64, merging per-block changes into a roaring64 index.

package state

import (
	"bytes"
	"fmt"
	"math"

	"github.com/RoaringBitmap/roaring/roaring64"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/changeset"
	"github.com/n42blockchain/N42/modules/ethdb/bitmapdb"
)

func originalAccountData(original *account.StateAccount, omitHashes bool, incarnation uint16) []byte {
	return EncodeAccountForHistory(original, omitHashes, incarnation)
}

func writeIndex(blocknum uint64, changes *changeset.ChangeSet, bucket string, changeDb kv.RwTx) error {
	buf := bytes.NewBuffer(nil)
	for _, change := range changes.Changes {
		k := modules.CompositeKeyWithoutIncarnation(change.Key)

		index, err := bitmapdb.Get64(changeDb, bucket, k, math.MaxUint32, math.MaxUint32)
		if err != nil {
			return fmt.Errorf("find chunk failed: %w", err)
		}
		index.Add(blocknum)
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
