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
// Log event index writer over LogAddressIndex and LogTopicIndex.
// WriteLogIndex extracts unique addresses and topics from a block's
// receipts and adds the block number into sharded roaring bitmaps
// keyed by addr(20)+shard(4) and topic(32)+shard(4). Shards cap the
// MDBX value size so oversized bitmaps stay under the page threshold
// by rolling into a new chunk when the bitmap grows too large.

package rawdb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"sort"

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

	logs := make([]*block.Log, 0, len(receipts))
	for _, r := range receipts {
		logs = append(logs, r.Logs...)
	}
	return writeLogIndexKeys(tx, blockNum, blockNum32, logs)
}

// writeLogIndexKeys is the shared body of WriteLogIndex and
// WriteLogIndexFromLogs.
//
// The keys are sorted before writing. Ranging over the dedup maps directly
// meant every block put into the two index trees in Go's randomized map order,
// so consecutive writes landed on unrelated B-tree pages; sorted keys walk the
// tree in order and keep the touched page set small.
func writeLogIndexKeys(tx kv.RwTx, blockNum uint64, blockNum32 uint32, logs []*block.Log) error {
	addrSet := make(map[types.Address]struct{})
	topicSet := make(map[types.Hash]struct{})
	for _, l := range logs {
		addrSet[l.Address] = struct{}{}
		for _, t := range l.Topics {
			topicSet[t] = struct{}{}
		}
	}

	addrs := make([][]byte, 0, len(addrSet))
	for addr := range addrSet {
		a := addr
		addrs = append(addrs, a[:])
	}
	topics := make([][]byte, 0, len(topicSet))
	for topic := range topicSet {
		t := topic
		topics = append(topics, t[:])
	}
	sort.Slice(addrs, func(i, j int) bool { return bytes.Compare(addrs[i], addrs[j]) < 0 })
	sort.Slice(topics, func(i, j int) bool { return bytes.Compare(topics[i], topics[j]) < 0 })

	buf := bytes.NewBuffer(nil)
	for _, addr := range addrs {
		if err := addToLogIndex(tx, modules.LogAddressIndex, addr, blockNum32, buf); err != nil {
			return fmt.Errorf("write log address index for block %d: %w", blockNum, err)
		}
	}
	for _, topic := range topics {
		if err := addToLogIndex(tx, modules.LogTopicIndex, topic, blockNum32, buf); err != nil {
			return fmt.Errorf("write log topic index for block %d: %w", blockNum, err)
		}
	}
	return nil
}

// openChunkKey builds the key of the one mutable ("open") chunk of a log-index
// entry. bitmapdb.WalkChunkWithKeys keys sealed chunks by their maximum block
// number and the trailing chunk by ^uint32(0), so the open chunk always sorts
// last and can be fetched with a single point read.
func openChunkKey(key []byte) []byte {
	k := make([]byte, len(key)+4)
	copy(k, key)
	binary.BigEndian.PutUint32(k[len(key):], ^uint32(0))
	return k
}

// addToLogIndex adds blockNum to the bitmap of `key`.
//
// Block numbers arrive in increasing order, and a chunk is sealed once it
// reaches ChunkLimit, so every sealed chunk is immutable: appending can only
// ever touch the open chunk. This reads that one chunk, adds the number, and
// writes it back — O(ChunkLimit) per key per block.
//
// It used to read every chunk of the whole history back to block 0, OR them
// together, add one number, re-chunk the entire bitmap and re-write every
// chunk, on every block. That was O(total history per key) on the block-commit
// critical path, and it dominated the node: 87% of all heap allocation and ~40%
// of import CPU went to the roaring intersect/union churn it caused (~20 MB of
// garbage per block on a chain doing one small block every 2 s).
func addToLogIndex(tx kv.RwTx, bucket string, key []byte, blockNum uint32, buf *bytes.Buffer) error {
	openKey := openChunkKey(key)
	v, err := tx.GetOne(bucket, openKey)
	if err != nil {
		return err
	}
	bm := roaring.New()
	if len(v) > 0 {
		if _, err := bm.ReadFrom(bytes.NewReader(v)); err != nil {
			return err
		}
	}

	// Deep reorg: a block number below the open chunk's floor would extend it
	// backwards past a sealed chunk's maximum, and bitmapdb.Get stops scanning
	// at the first chunk whose key reaches its `to` bound — so a stray low
	// number parked in the open chunk could be missed by a bounded range query.
	// Rare enough to just fall back to the old full rewrite, which re-derives
	// every boundary.
	if bm.GetCardinality() > 0 && blockNum < bm.Minimum() {
		return rewriteLogIndex(tx, bucket, key, blockNum, buf)
	}

	bm.Add(blockNum)
	bm.RunOptimize()

	if bm.GetSerializedSizeInBytes() <= bitmapdb.ChunkLimit {
		return putBitmap(tx, bucket, openKey, bm, buf)
	}

	// Over the limit: seal the left part under its own maximum and keep the
	// remainder as the open chunk. CutLeft leaves at least the maximum behind,
	// so the open chunk is never emptied here.
	left := bitmapdb.CutLeft(bm, bitmapdb.ChunkLimit)
	if left == nil || left.GetCardinality() == 0 {
		return putBitmap(tx, bucket, openKey, bm, buf)
	}
	sealedKey := make([]byte, len(key)+4)
	copy(sealedKey, key)
	binary.BigEndian.PutUint32(sealedKey[len(key):], left.Maximum())
	if err := putBitmap(tx, bucket, sealedKey, left, buf); err != nil {
		return err
	}
	return putBitmap(tx, bucket, openKey, bm, buf)
}

// rewriteLogIndex is the original whole-history read-modify-write. It is now
// only the deep-reorg fallback of addToLogIndex, where chunk boundaries have to
// be recomputed from scratch.
func rewriteLogIndex(tx kv.RwTx, bucket string, key []byte, blockNum uint32, buf *bytes.Buffer) error {
	bm, err := bitmapdb.Get(tx, bucket, key, 0, math.MaxUint32)
	if err != nil {
		return err
	}
	bm.Add(blockNum)
	return bitmapdb.WalkChunkWithKeys(key, bm, bitmapdb.ChunkLimit, func(chunkKey []byte, chunk *roaring.Bitmap) error {
		return putBitmap(tx, bucket, chunkKey, chunk, buf)
	})
}

func putBitmap(tx kv.RwTx, bucket string, chunkKey []byte, bm *roaring.Bitmap, buf *bytes.Buffer) error {
	buf.Reset()
	if _, err := bm.WriteTo(buf); err != nil {
		return err
	}
	return tx.Put(bucket, chunkKey, types.CopyBytes(buf.Bytes()))
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
	return writeLogIndexKeys(tx, blockNum, uint32(blockNum), logs)
}
