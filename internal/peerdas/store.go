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
// MDBX-backed column store for PeerDAS. Uses a 40-byte key
// (blockHash[32] || columnIndex[big-endian uint64]) via the columnKey
// helper so columns for a given block are contiguous. Wraps kv.RwDB
// read/write transactions to put, get and range-iterate custody
// columns without pulling the whole blob back into memory.

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
// Format v2:
//
//	blockNumber(8) +
//	blobCount(4) +
//	for each blob:
//	  commitment(KZGCommitmentLength) +
//	  proof(KZGProofLength) +
//	  cell(BytesPerCell)
func encodeColumn(col *DataColumn) ([]byte, error) {
	blobCount := len(col.Cells)
	if blobCount != len(col.Commitments) || blobCount != len(col.KZGProofs) {
		return nil, fmt.Errorf("peerdas: slice length mismatch: cells=%d commitments=%d proofs=%d",
			blobCount, len(col.Commitments), len(col.KZGProofs))
	}
	// Fixed-size per blob: commitment(48) + proof(48) + cell(2048) = 2144
	perBlob := KZGCommitmentLength + KZGProofLength + BytesPerCell
	size := 8 + 4 + blobCount*perBlob

	buf := make([]byte, 0, size)

	// Block number (8 bytes).
	var numBuf [8]byte
	binary.BigEndian.PutUint64(numBuf[:], col.BlockNumber)
	buf = append(buf, numBuf[:]...)

	// Blob count (4 bytes).
	var countBuf [4]byte
	binary.BigEndian.PutUint32(countBuf[:], uint32(blobCount))
	buf = append(buf, countBuf[:]...)

	// Per-blob data: commitment + proof + cell.
	for i := 0; i < blobCount; i++ {
		buf = append(buf, col.Commitments[i]...)
		buf = append(buf, col.KZGProofs[i]...)
		buf = append(buf, col.Cells[i]...)
	}

	return buf, nil
}

// decodeColumn deserializes a DataColumn from bytes.
func decodeColumn(blockHash types.Hash, colIdx uint64, raw []byte) (*DataColumn, error) {
	if len(raw) < 12 { // 8 (blockNum) + 4 (blobCount) minimum
		return nil, fmt.Errorf("peerdas: column data too short: %d bytes", len(raw))
	}

	offset := 0

	blockNum := binary.BigEndian.Uint64(raw[offset:])
	offset += 8

	blobCount := int(binary.BigEndian.Uint32(raw[offset:]))
	offset += 4

	if blobCount > 1024 { // reasonable upper bound
		return nil, fmt.Errorf("peerdas: blob count %d exceeds limit", blobCount)
	}

	perBlob := KZGCommitmentLength + KZGProofLength + BytesPerCell
	expectedRemaining := blobCount * perBlob
	if offset+expectedRemaining > len(raw) {
		return nil, fmt.Errorf("peerdas: data truncated: need %d bytes, have %d", offset+expectedRemaining, len(raw))
	}

	commitments := make([][]byte, blobCount)
	proofs := make([][]byte, blobCount)
	cells := make([][]byte, blobCount)

	for i := 0; i < blobCount; i++ {
		commitment := make([]byte, KZGCommitmentLength)
		copy(commitment, raw[offset:offset+KZGCommitmentLength])
		offset += KZGCommitmentLength

		proof := make([]byte, KZGProofLength)
		copy(proof, raw[offset:offset+KZGProofLength])
		offset += KZGProofLength

		cell := make([]byte, BytesPerCell)
		copy(cell, raw[offset:offset+BytesPerCell])
		offset += BytesPerCell

		commitments[i] = commitment
		proofs[i] = proof
		cells[i] = cell
	}

	return &DataColumn{
		Index:       colIdx,
		BlockHash:   blockHash,
		BlockNumber: blockNum,
		Cells:       cells,
		KZGProofs:   proofs,
		Commitments: commitments,
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
