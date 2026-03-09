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
	"encoding/binary"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/api/protocol/types_pb"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// ReadReceiptsBatch reads receipts for a contiguous range of blocks [fromBlock, toBlock]
// using a single cursor scan over the Receipts table. This is significantly more
// efficient than calling ReadRawReceipts individually for each block number because:
//   - A cursor scan is sequential I/O, friendly to MDBX's memory-mapped B+ tree pages
//   - Only one cursor is opened instead of N individual GetOne calls
//   - Key comparison is cheaper than repeated table lookups
//
// Returns a map keyed by block number. Blocks with no receipts are omitted from the map.
func ReadReceiptsBatch(tx kv.Tx, fromBlock, toBlock uint64) (map[uint64][]*block.Receipt, error) {
	if fromBlock > toBlock {
		return nil, fmt.Errorf("ReadReceiptsBatch: fromBlock (%d) > toBlock (%d)", fromBlock, toBlock)
	}

	result := make(map[uint64][]*block.Receipt, toBlock-fromBlock+1)

	c, err := tx.Cursor(modules.Receipts)
	if err != nil {
		return nil, fmt.Errorf("ReadReceiptsBatch: cursor open failed: %w", err)
	}
	defer c.Close()

	fromKey := modules.EncodeBlockNumber(fromBlock)
	toKey := modules.EncodeBlockNumber(toBlock)

	for k, v, err := c.Seek(fromKey); k != nil; k, v, err = c.Next() {
		if err != nil {
			return nil, fmt.Errorf("ReadReceiptsBatch: cursor iteration failed: %w", err)
		}
		// Stop if we've passed the end of the requested range.
		if bytes.Compare(k, toKey) > 0 {
			break
		}
		if len(k) != 8 {
			continue
		}
		if len(v) == 0 {
			continue
		}

		blockNum := binary.BigEndian.Uint64(k)

		var receipts block.Receipts
		if err := receipts.Unmarshal(v); err != nil {
			return nil, fmt.Errorf("ReadReceiptsBatch: unmarshal receipts at block %d: %w", blockNum, err)
		}
		result[blockNum] = receipts
	}

	return result, nil
}

// ReadLazyReceiptsBatch is like ReadReceiptsBatch but returns LazyReceipt
// wrappers instead of fully-parsed receipts. Useful when callers only need
// a subset of receipt fields (e.g., status checks during sync verification).
//
// IMPORTANT: The returned LazyReceipts reference MDBX memory-mapped pages.
// They are only valid for the lifetime of the enclosing read transaction.
// Call LazyReceipt.Copy() on any receipt that must outlive the transaction.
func ReadLazyReceiptsBatch(tx kv.Tx, fromBlock, toBlock uint64) (map[uint64][]*LazyReceipt, error) {
	if fromBlock > toBlock {
		return nil, fmt.Errorf("ReadLazyReceiptsBatch: fromBlock (%d) > toBlock (%d)", fromBlock, toBlock)
	}

	result := make(map[uint64][]*LazyReceipt, toBlock-fromBlock+1)

	c, err := tx.Cursor(modules.Receipts)
	if err != nil {
		return nil, fmt.Errorf("ReadLazyReceiptsBatch: cursor open failed: %w", err)
	}
	defer c.Close()

	fromKey := modules.EncodeBlockNumber(fromBlock)
	toKey := modules.EncodeBlockNumber(toBlock)

	for k, v, err := c.Seek(fromKey); k != nil; k, v, err = c.Next() {
		if err != nil {
			return nil, fmt.Errorf("ReadLazyReceiptsBatch: cursor iteration failed: %w", err)
		}
		if bytes.Compare(k, toKey) > 0 {
			break
		}
		if len(k) != 8 {
			continue
		}
		if len(v) == 0 {
			continue
		}

		blockNum := binary.BigEndian.Uint64(k)

		// Unmarshal just the top-level Receipts wrapper to get individual receipt bytes.
		pbReceipts := new(types_pb.Receipts)
		if err := proto.Unmarshal(v, pbReceipts); err != nil {
			return nil, fmt.Errorf("ReadLazyReceiptsBatch: unmarshal at block %d: %w", blockNum, err)
		}

		lazy := make([]*LazyReceipt, 0, len(pbReceipts.Receipts))
		for _, pbR := range pbReceipts.Receipts {
			raw, err := proto.Marshal(pbR)
			if err != nil {
				return nil, fmt.Errorf("ReadLazyReceiptsBatch: re-marshal receipt at block %d: %w", blockNum, err)
			}
			lazy = append(lazy, NewLazyReceipt(raw))
		}
		result[blockNum] = lazy
	}

	return result, nil
}

// ReadHeadersBatch reads headers for a contiguous range of blocks [fromBlock, toBlock]
// using a cursor scan over the Headers table. More efficient than individual
// ReadHeader calls when processing sequential blocks (e.g., during chain sync
// or data export).
//
// Returns a map keyed by block number. Only canonical headers (matching the
// canonical hash for each block number) are included.
func ReadHeadersBatch(tx kv.Tx, fromBlock, toBlock uint64) (map[uint64]*block.Header, error) {
	if fromBlock > toBlock {
		return nil, fmt.Errorf("ReadHeadersBatch: fromBlock (%d) > toBlock (%d)", fromBlock, toBlock)
	}

	result := make(map[uint64]*block.Header, toBlock-fromBlock+1)

	c, err := tx.Cursor(modules.Headers)
	if err != nil {
		return nil, fmt.Errorf("ReadHeadersBatch: cursor open failed: %w", err)
	}
	defer c.Close()

	fromPrefix := modules.EncodeBlockNumber(fromBlock)

	for k, v, err := c.Seek(fromPrefix); k != nil; k, v, err = c.Next() {
		if err != nil {
			return nil, fmt.Errorf("ReadHeadersBatch: cursor iteration failed: %w", err)
		}
		// Header keys are 8 (number) + 32 (hash) = 40 bytes.
		if len(k) != 40 {
			continue
		}

		blockNum := binary.BigEndian.Uint64(k[:8])
		if blockNum > toBlock {
			break
		}

		if len(v) == 0 {
			continue
		}

		// Only include canonical headers: verify the hash in the key matches
		// the canonical hash for this block number.
		canonHash, err := ReadCanonicalHash(tx, blockNum)
		if err != nil {
			return nil, fmt.Errorf("ReadHeadersBatch: read canonical hash for block %d: %w", blockNum, err)
		}
		keyHash := k[8:]
		if !bytes.Equal(keyHash, canonHash[:]) {
			continue
		}

		header := new(block.Header)
		pbHeader := new(types_pb.Header)
		if err := proto.Unmarshal(v, pbHeader); err != nil {
			return nil, fmt.Errorf("ReadHeadersBatch: unmarshal header at block %d: %w", blockNum, err)
		}
		if err := header.FromProtoMessage(pbHeader); err != nil {
			return nil, fmt.Errorf("ReadHeadersBatch: convert header at block %d: %w", blockNum, err)
		}
		result[blockNum] = header
	}

	return result, nil
}
