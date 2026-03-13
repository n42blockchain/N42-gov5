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
	"encoding/binary"
	"fmt"
	"math"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

// HasReceipts verifies the existence of all the transaction receipts belonging
// to a block.
func HasReceipts(db kv.Has, number uint64) bool {
	has, err := db.Has(modules.Receipts, modules.EncodeBlockNumber(number))
	return has && err == nil
}

// ReadRawReceipts retrieves all the transaction receipts belonging to a block.
// The receipt metadata fields are not guaranteed to be populated, so they
// should not be used. Use ReadReceipts instead if the metadata is needed.
func ReadRawReceipts(db kv.Tx, blockNum uint64) block.Receipts {
	data, err := db.GetOne(modules.Receipts, modules.EncodeBlockNumber(blockNum))
	if err != nil {
		log.Error("ReadRawReceipts failed", "err", err)
	}
	if len(data) == 0 {
		return nil
	}
	var receipts block.Receipts
	if err := receipts.Unmarshal(data); err != nil {
		log.Error("ReadRawReceipts failed", "err", err)
		return nil
	}
	return receipts
}

// ReadReceipts retrieves all the transaction receipts belonging to a block, including
// its corresponding metadata fields. If it is unable to populate these metadata
// fields then nil is returned.
//
// The current implementation populates these metadata fields by reading the receipts'
// corresponding block body, so if the block body is not found it will return nil even
// if the receipt itself is stored.
func ReadReceipts(db kv.Tx, block *block.Block, senders []types.Address) block.Receipts {
	if block == nil {
		return nil
	}
	receipts := ReadRawReceipts(db, block.Number64().Uint64())
	if receipts == nil {
		return nil
	}
	if len(senders) > 0 {
		block.SendersToTxs(senders)
	}
	return receipts
}

func ReadReceiptsByHash(db kv.Tx, hash types.Hash) (block.Receipts, error) {
	number := ReadHeaderNumber(db, hash)
	if number == nil {
		return nil, nil
	}
	canonicalHash, err := ReadCanonicalHash(db, *number)
	if err != nil {
		return nil, fmt.Errorf("requested non-canonical hash %x. canonical=%x", hash, canonicalHash)
	}
	b, s, err := ReadBlockWithSenders(db, hash, *number)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, nil
	}
	receipts := ReadReceipts(db, b, s)
	if receipts == nil {
		return nil, nil
	}
	return receipts, nil
}

// WriteReceipts stores all the transaction receipts belonging to a block.
func WriteReceipts(tx kv.Putter, number uint64, receipts block.Receipts) error {
	for txID, receipt := range receipts {
		if len(receipt.Logs) == 0 {
			continue
		}
		logs := block.Logs(receipt.Logs)
		data, err := logs.Marshal()
		if err != nil {
			return fmt.Errorf("encode block logs for block %d: %w", number, err)
		}
		if err = tx.Put(modules.Log, modules.LogKey(number, uint32(txID)), data); err != nil {
			return fmt.Errorf("writing logs for block %d: %w", number, err)
		}
	}

	data, err := receipts.Marshal()
	if err != nil {
		return fmt.Errorf("encode block receipts for block %d: %w", number, err)
	}
	if err = tx.Put(modules.Receipts, modules.EncodeBlockNumber(number), data); err != nil {
		return fmt.Errorf("writing receipts for block %d: %w", number, err)
	}
	return nil
}

// WriteReceiptsPooled is like WriteReceipts but reuses pooled byte buffers
// for the receipt serialization, reducing per-call allocations on the hot
// block-processing path.
func WriteReceiptsPooled(tx kv.Putter, number uint64, receipts block.Receipts) error {
	for txID, receipt := range receipts {
		if len(receipt.Logs) == 0 {
			continue
		}
		logs := block.Logs(receipt.Logs)
		data, err := logs.Marshal()
		if err != nil {
			return fmt.Errorf("encode block logs for block %d: %w", number, err)
		}
		if err = tx.Put(modules.Log, modules.LogKey(number, uint32(txID)), data); err != nil {
			return fmt.Errorf("writing logs for block %d: %w", number, err)
		}
	}

	buf := GetBuf()
	defer PutBuf(buf)

	data, err := receipts.Marshal()
	if err != nil {
		return fmt.Errorf("encode block receipts for block %d: %w", number, err)
	}
	*buf = append(*buf, data...)
	if err = tx.Put(modules.Receipts, modules.EncodeBlockNumber(number), *buf); err != nil {
		return fmt.Errorf("writing receipts for block %d: %w", number, err)
	}
	return nil
}

// AppendReceipts stores all the transaction receipts belonging to a block.
func AppendReceipts(tx kv.StatelessWriteTx, blockNumber uint64, receipts block.Receipts) error {
	for txID, receipt := range receipts {
		if len(receipt.Logs) == 0 {
			continue
		}
		logs := block.Logs(receipt.Logs)
		data, err := logs.Marshal()
		if err != nil {
			return err
		}
		if err = tx.Append(modules.Log, modules.LogKey(blockNumber, uint32(txID)), data); err != nil {
			return fmt.Errorf("writing logs for block %d: %w", blockNumber, err)
		}
	}

	data, err := receipts.Marshal()
	if err != nil {
		return fmt.Errorf("encode block receipts for block %d: %w", blockNumber, err)
	}
	if err = tx.Append(modules.Receipts, modules.EncodeBlockNumber(blockNumber), data); err != nil {
		return fmt.Errorf("writing receipts for block %d: %w", blockNumber, err)
	}
	return nil
}

// TruncateReceipts removes all receipt for given block number or newer.
func TruncateReceipts(db kv.RwTx, number uint64) error {
	prefix := modules.EncodeBlockNumber(number)
	if err := db.ForEach(modules.Receipts, prefix, func(k, _ []byte) error {
		return db.Delete(modules.Receipts, k)
	}); err != nil {
		return err
	}
	if err := db.ForEach(modules.Log, prefix, func(k, _ []byte) error {
		return db.Delete(modules.Log, k)
	}); err != nil {
		return err
	}
	return nil
}

func ReceiptsAvailableFrom(tx kv.Tx) (uint64, error) {
	c, err := tx.Cursor(modules.Receipts)
	if err != nil {
		return math.MaxUint64, err
	}
	defer c.Close()
	k, _, err := c.First()
	if err != nil {
		return math.MaxUint64, err
	}
	if len(k) == 0 {
		return math.MaxUint64, nil
	}
	return binary.BigEndian.Uint64(k), nil
}
