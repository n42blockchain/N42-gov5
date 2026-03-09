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

package rawdb

import (
	"bytes"
	"fmt"
	"math"

	"github.com/RoaringBitmap/roaring"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/ethdb/bitmapdb"
)

// WriteLogIndex writes log indices for a block's receipts into LogTopicIndex and
// LogAddressIndex tables.
//
// For each receipt log, it extracts unique addresses and topics, then adds the
// block number to the corresponding roaring bitmap in the database. The bitmaps
// are chunked to avoid oversized MDBX values: the shard key suffix is a 4-byte
// big-endian uint32 equal to the chunk's maximum block number (see bitmapdb
// package).
//
// Key format:
//
//	LogAddressIndex: address(20) + shard(4) -> roaring_bitmap
//	LogTopicIndex:   topic(32)   + shard(4) -> roaring_bitmap
func WriteLogIndex(tx kv.RwTx, blockNum uint64, receipts block.Receipts) error {
	if blockNum > math.MaxUint32 {
		return fmt.Errorf("block number %d exceeds uint32 range for roaring bitmap", blockNum)
	}
	blockNum32 := uint32(blockNum)

	// Collect unique addresses and topics from all logs.
	addrSet := make(map[types.Address]struct{})
	topicSet := make(map[types.Hash]struct{})
	for _, r := range receipts {
		for _, l := range r.Logs {
			addrSet[l.Address] = struct{}{}
			for _, t := range l.Topics {
				topicSet[t] = struct{}{}
			}
		}
	}

	buf := bytes.NewBuffer(nil)

	// Update address index.
	for addr := range addrSet {
		if err := addToLogIndex(tx, modules.LogAddressIndex, addr[:], blockNum32, buf); err != nil {
			return fmt.Errorf("write log address index for block %d: %w", blockNum, err)
		}
	}

	// Update topic index.
	for topic := range topicSet {
		if err := addToLogIndex(tx, modules.LogTopicIndex, topic[:], blockNum32, buf); err != nil {
			return fmt.Errorf("write log topic index for block %d: %w", blockNum, err)
		}
	}

	return nil
}

// addToLogIndex reads the existing bitmap for `key` from `bucket`, adds
// `blockNum`, then re-writes the (potentially chunked) bitmap back to the DB.
func addToLogIndex(tx kv.RwTx, bucket string, key []byte, blockNum uint32, buf *bytes.Buffer) error {
	// Read existing bitmap covering [0, MaxUint32].
	bm, err := bitmapdb.Get(tx, bucket, key, 0, math.MaxUint32)
	if err != nil {
		return err
	}
	bm.Add(blockNum)

	// Re-write chunked bitmap.
	return bitmapdb.WalkChunkWithKeys(key, bm, bitmapdb.ChunkLimit, func(chunkKey []byte, chunk *roaring.Bitmap) error {
		buf.Reset()
		if _, err := chunk.WriteTo(buf); err != nil {
			return err
		}
		return tx.Put(bucket, chunkKey, types.CopyBytes(buf.Bytes()))
	})
}

// PruneLogIndex removes all log index entries whose shard key (maximum block
// number in the chunk) is less than `to`. Entries with block numbers crossing
// the boundary are trimmed rather than deleted entirely.
//
// The range semantics is: remove all indexed block numbers in [0, to).
func PruneLogIndex(tx kv.RwTx, to uint32) error {
	for _, bucket := range []string{modules.LogAddressIndex, modules.LogTopicIndex} {
		if err := pruneLogIndexBucket(tx, bucket, to); err != nil {
			return err
		}
	}
	return nil
}

func pruneLogIndexBucket(tx kv.RwTx, bucket string, to uint32) error {
	// Collect all distinct key prefixes (address or topic) that have entries.
	prefixes, err := collectLogIndexPrefixes(tx, bucket)
	if err != nil {
		return err
	}

	buf := bytes.NewBuffer(nil)

	for _, prefix := range prefixes {
		// Read full bitmap for this key across all chunks.
		bm, err := bitmapdb.Get(tx, bucket, prefix, 0, math.MaxUint32)
		if err != nil {
			return err
		}
		if bm.IsEmpty() {
			continue
		}

		// If all entries are below the prune point, delete everything for this key.
		if bm.Maximum() < to {
			if err := deleteAllChunks(tx, bucket, prefix); err != nil {
				return err
			}
			continue
		}

		// Remove values in [0, to).
		if bm.Minimum() < to {
			bm.RemoveRange(0, uint64(to))
			// Delete old chunks, then write the trimmed bitmap back.
			if err := deleteAllChunks(tx, bucket, prefix); err != nil {
				return err
			}
			if err := bitmapdb.WalkChunkWithKeys(prefix, bm, bitmapdb.ChunkLimit, func(chunkKey []byte, chunk *roaring.Bitmap) error {
				buf.Reset()
				if _, err := chunk.WriteTo(buf); err != nil {
					return err
				}
				return tx.Put(bucket, chunkKey, types.CopyBytes(buf.Bytes()))
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// collectLogIndexPrefixes scans the given bucket and returns all distinct key
// prefixes (everything before the 4-byte shard suffix).
func collectLogIndexPrefixes(tx kv.Tx, bucket string) ([][]byte, error) {
	c, err := tx.Cursor(bucket)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	seen := make(map[string]struct{})
	var prefixes [][]byte

	for k, _, err := c.First(); k != nil; k, _, err = c.Next() {
		if err != nil {
			return nil, err
		}
		if len(k) < 4 {
			continue
		}
		prefix := k[:len(k)-4]
		key := string(prefix)
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			cp := make([]byte, len(prefix))
			copy(cp, prefix)
			prefixes = append(prefixes, cp)
		}
	}
	return prefixes, nil
}

// deleteAllChunks removes all entries with the given key prefix from the bucket.
func deleteAllChunks(tx kv.RwTx, bucket string, prefix []byte) error {
	c, err := tx.RwCursor(bucket)
	if err != nil {
		return err
	}
	defer c.Close()

	for k, _, err := c.Seek(prefix); k != nil; k, _, err = c.Next() {
		if err != nil {
			return err
		}
		if !bytes.HasPrefix(k, prefix) {
			break
		}
		if err := c.DeleteCurrent(); err != nil {
			return err
		}
	}
	return nil
}

// WriteLogIndexFromLogs is a convenience variant of WriteLogIndex that accepts
// raw logs instead of full receipts. It is useful when the caller already has
// the logs extracted (e.g. from receipt processing).
func WriteLogIndexFromLogs(tx kv.RwTx, blockNum uint64, logs []*block.Log) error {
	if blockNum > math.MaxUint32 {
		return fmt.Errorf("block number %d exceeds uint32 range for roaring bitmap", blockNum)
	}
	blockNum32 := uint32(blockNum)

	addrSet := make(map[types.Address]struct{})
	topicSet := make(map[types.Hash]struct{})
	for _, l := range logs {
		addrSet[l.Address] = struct{}{}
		for _, t := range l.Topics {
			topicSet[t] = struct{}{}
		}
	}

	buf := bytes.NewBuffer(nil)

	for addr := range addrSet {
		if err := addToLogIndex(tx, modules.LogAddressIndex, addr[:], blockNum32, buf); err != nil {
			return fmt.Errorf("write log address index for block %d: %w", blockNum, err)
		}
	}
	for topic := range topicSet {
		if err := addToLogIndex(tx, modules.LogTopicIndex, topic[:], blockNum32, buf); err != nil {
			return fmt.Errorf("write log topic index for block %d: %w", blockNum, err)
		}
	}
	return nil
}
