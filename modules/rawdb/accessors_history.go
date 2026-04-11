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
// Earliest-block pointer and bulk block deletion helpers.
// ReadEarliestBlock/WriteEarliestBlock persist the lowest available
// block number under modules.DatabaseInfo for pruning clients.
// DeleteBlockData removes header, body, receipts, logs, senders and
// total difficulty records for a single (number,hash) pair in a
// single transaction during history pruning.

package rawdb

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// earliestBlockKey is the DB key under which the earliest available block
// number is persisted (in the DatabaseInfo table).
var earliestBlockKey = []byte("EarliestBlock")

// ReadEarliestBlock reads the earliest available block number from the DB.
// Returns 0 if the key is not set (i.e. all history is available).
func ReadEarliestBlock(tx kv.Getter) (uint64, error) {
	data, err := tx.GetOne(modules.DatabaseInfo, earliestBlockKey)
	if err != nil {
		return 0, fmt.Errorf("ReadEarliestBlock: %w", err)
	}
	if len(data) == 0 {
		return 0, nil
	}
	if len(data) != 8 {
		return 0, fmt.Errorf("ReadEarliestBlock: invalid data length %d", len(data))
	}
	return binary.BigEndian.Uint64(data), nil
}

// WriteEarliestBlock writes the earliest available block number to the DB.
func WriteEarliestBlock(tx kv.Putter, blockNum uint64) error {
	val := make([]byte, 8)
	binary.BigEndian.PutUint64(val, blockNum)
	if err := tx.Put(modules.DatabaseInfo, earliestBlockKey, val); err != nil {
		return fmt.Errorf("WriteEarliestBlock: %w", err)
	}
	return nil
}

// DeleteBlockData deletes all block-level data (header, body, receipts, logs,
// senders, TD) for a single block identified by number and hash.
//
// It intentionally preserves:
//   - HeaderCanonical (canonical chain mapping is still needed)
//   - HeaderNumber (hash -> number lookup)
//   - TxLookup (tx hash -> block number lookup)
func DeleteBlockData(tx kv.RwTx, blockNum uint64, hash types.Hash) error {
	blockKey := modules.HeaderKey(blockNum, hash)
	numKey := modules.EncodeBlockNumber(blockNum)

	// 1. Delete header
	if err := tx.Delete(modules.Headers, blockKey); err != nil {
		return fmt.Errorf("delete header %d: %w", blockNum, err)
	}

	// 2. Delete header total difficulty
	if err := tx.Delete(modules.HeaderTD, blockKey); err != nil {
		return fmt.Errorf("delete header TD %d: %w", blockNum, err)
	}

	// 3. Delete block body
	if err := tx.Delete(modules.BlockBody, blockKey); err != nil {
		return fmt.Errorf("delete block body %d: %w", blockNum, err)
	}

	// 4. Delete receipts
	if err := tx.Delete(modules.Receipts, numKey); err != nil {
		return fmt.Errorf("delete receipts %d: %w", blockNum, err)
	}

	// 5. Delete logs - logs are keyed by block_num(8) + tx_id(4), so we
	// need to iterate over all entries with the block number prefix.
	if err := deleteLogsByBlockNum(tx, blockNum); err != nil {
		return fmt.Errorf("delete logs %d: %w", blockNum, err)
	}

	// 6. Delete senders
	if err := tx.Delete(modules.Senders, blockKey); err != nil {
		return fmt.Errorf("delete senders %d: %w", blockNum, err)
	}

	return nil
}

// deleteLogsByBlockNum deletes all log entries for a given block number.
// Log keys are block_num(8) + tx_id(4).
func deleteLogsByBlockNum(tx kv.RwTx, blockNum uint64) error {
	prefix := modules.EncodeBlockNumber(blockNum)
	c, err := tx.RwCursor(modules.Log)
	if err != nil {
		return err
	}
	defer c.Close()

	for k, _, err := c.Seek(prefix); k != nil; k, _, err = c.Next() {
		if err != nil {
			return err
		}
		// Log keys are 12 bytes: block_num(8) + tx_id(4).
		// If key no longer starts with our block number prefix, stop.
		if len(k) < 8 {
			break
		}
		if binary.BigEndian.Uint64(k[:8]) != blockNum {
			break
		}
		if err := c.DeleteCurrent(); err != nil {
			return err
		}
	}
	return nil
}
