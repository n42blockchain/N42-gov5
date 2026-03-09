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

package peerdas

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// columnKey builds the database key for a data column:
// blockHash (32 bytes) + column index (8 bytes big-endian) = 40 bytes.
func columnKey(blockHash types.Hash, colIdx uint64) []byte {
	key := make([]byte, 40)
	copy(key[:32], blockHash[:])
	binary.BigEndian.PutUint64(key[32:], colIdx)
	return key
}

// encodeColumn serializes a DataColumn to bytes.
// Format: blockNumber(8) + kzgProofLen(4) + kzgProof + dataCount(4) + for each data: len(4) + bytes.
func encodeColumn(col *DataColumn) ([]byte, error) {
	// Calculate total size.
	size := 8 + 4 + len(col.KZGProof) + 4
	for _, d := range col.Data {
		size += 4 + len(d)
	}

	buf := make([]byte, 0, size)

	// Block number (8 bytes).
	var numBuf [8]byte
	binary.BigEndian.PutUint64(numBuf[:], col.BlockNumber)
	buf = append(buf, numBuf[:]...)

	// KZG proof length + data.
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(col.KZGProof)))
	buf = append(buf, lenBuf[:]...)
	buf = append(buf, col.KZGProof...)

	// Data slices count.
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(col.Data)))
	buf = append(buf, lenBuf[:]...)

	for _, d := range col.Data {
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(d)))
		buf = append(buf, lenBuf[:]...)
		buf = append(buf, d...)
	}

	return buf, nil
}

// decodeColumn deserializes a DataColumn from bytes.
func decodeColumn(blockHash types.Hash, colIdx uint64, raw []byte) (*DataColumn, error) {
	if len(raw) < 12 { // 8 (blockNum) + 4 (proofLen) minimum
		return nil, fmt.Errorf("peerdas: column data too short: %d bytes", len(raw))
	}

	offset := 0

	blockNum := binary.BigEndian.Uint64(raw[offset:])
	offset += 8

	proofLen := int(binary.BigEndian.Uint32(raw[offset:]))
	offset += 4
	if offset+proofLen > len(raw) {
		return nil, fmt.Errorf("peerdas: proof length %d exceeds data", proofLen)
	}
	proof := make([]byte, proofLen)
	copy(proof, raw[offset:offset+proofLen])
	offset += proofLen

	if offset+4 > len(raw) {
		return nil, fmt.Errorf("peerdas: missing data count at offset %d", offset)
	}
	dataCount := int(binary.BigEndian.Uint32(raw[offset:]))
	offset += 4

	if dataCount > 1024 { // reasonable upper bound
		return nil, fmt.Errorf("peerdas: data count %d exceeds limit", dataCount)
	}

	data := make([][]byte, dataCount)
	for i := 0; i < dataCount; i++ {
		if offset+4 > len(raw) {
			return nil, fmt.Errorf("peerdas: truncated data entry %d", i)
		}
		dLen := int(binary.BigEndian.Uint32(raw[offset:]))
		offset += 4
		if offset+dLen > len(raw) {
			return nil, fmt.Errorf("peerdas: data entry %d length %d exceeds remaining", i, dLen)
		}
		entry := make([]byte, dLen)
		copy(entry, raw[offset:offset+dLen])
		offset += dLen
		data[i] = entry
	}

	return &DataColumn{
		Index:       colIdx,
		BlockHash:   blockHash,
		BlockNumber: blockNum,
		Data:        data,
		KZGProof:    proof,
	}, nil
}

// StoreColumn persists a data column to the database.
func StoreColumn(tx kv.RwTx, col *DataColumn) error {
	if col == nil {
		return ErrNilColumn
	}
	if col.Index >= NumberOfColumns {
		return ErrColumnIndexOutOfRange
	}

	encoded, err := encodeColumn(col)
	if err != nil {
		return fmt.Errorf("peerdas: encode column: %w", err)
	}

	key := columnKey(col.BlockHash, col.Index)
	return tx.Put(modules.DataColumns, key, encoded)
}

// GetColumn retrieves a stored data column by block hash and column index.
// Returns nil, nil if the column does not exist.
func GetColumn(tx kv.Tx, blockHash types.Hash, colIdx uint64) (*DataColumn, error) {
	if colIdx >= NumberOfColumns {
		return nil, ErrColumnIndexOutOfRange
	}

	key := columnKey(blockHash, colIdx)
	val, err := tx.GetOne(modules.DataColumns, key)
	if err != nil {
		return nil, fmt.Errorf("peerdas: get column: %w", err)
	}
	if val == nil {
		return nil, nil
	}
	return decodeColumn(blockHash, colIdx, val)
}

// HasColumn checks whether a data column exists in the database.
func HasColumn(tx kv.Tx, blockHash types.Hash, colIdx uint64) (bool, error) {
	if colIdx >= NumberOfColumns {
		return false, ErrColumnIndexOutOfRange
	}

	key := columnKey(blockHash, colIdx)
	return tx.Has(modules.DataColumns, key)
}
