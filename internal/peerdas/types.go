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
// Core PeerDAS data types and EIP-7594 constants: NumberOfColumns
// (128), CustodyRequirement (4), SamplesPerSlot (8) and the
// DataColumn / ColumnSidecar structs shared across custody, producer
// and store. Defines the wire representation used by the libp2p
// custody streaming protocol.

package peerdas

import "github.com/n42blockchain/N42/common/types"

const (
	// NumberOfColumns is the total number of data columns per blob (EIP-7594).
	NumberOfColumns = 128

	// CustodyRequirement is the minimum number of columns each node must custody.
	CustodyRequirement = 4

	// SamplesPerSlot is the default number of columns sampled for an availability check.
	SamplesPerSlot = 8

	// KZGProofLength is the expected length of a BLS12-381 G1 point used as a KZG proof.
	KZGProofLength = 48

	// KZGCommitmentLength is the expected length of a KZG commitment.
	KZGCommitmentLength = 48

	// BytesPerCell is the size of a single cell (64 field elements * 32 bytes).
	BytesPerCell = 2048

	// SamplingInterval is the default interval between sampling rounds.
	SamplingInterval = 12 // seconds (one slot)
)

// DataColumn represents a single column of blob data for a given block.
// In EIP-7594, each column index corresponds to one cell per blob in the block.
// For a block with N blobs, the column stores N cells, N KZG proofs, and N commitments.
type DataColumn struct {
	Index       uint64     // column index in [0, NumberOfColumns)
	BlockHash   types.Hash // block this column belongs to
	BlockNumber uint64

	// Cells holds the cell data for this column index, one per blob in the block.
	// Each entry is BytesPerCell (2048) bytes.
	Cells [][]byte

	// KZGProofs holds one KZG proof per blob for this column index.
	// Each entry is KZGProofLength (48) bytes.
	KZGProofs [][]byte

	// Commitments holds the KZG commitment for each blob.
	// Each entry is KZGCommitmentLength (48) bytes.
	// Required for KZG cell proof verification.
	Commitments [][]byte
}

// DataColumnIdentifier uniquely identifies a data column by block root
// and column index. Used in P2P requests.
type DataColumnIdentifier struct {
	BlockRoot types.Hash
	Index     uint64
}

// Validate performs structural validation on a DataColumn.
func (dc *DataColumn) Validate() error {
	if dc == nil {
		return ErrNilColumn
	}
	if dc.Index >= NumberOfColumns {
		return ErrColumnIndexOutOfRange
	}
	if dc.BlockHash == (types.Hash{}) {
		return ErrEmptyBlockHash
	}
	if len(dc.Cells) == 0 {
		return ErrEmptyColumnData
	}

	// Validate cell sizes.
	for i, cell := range dc.Cells {
		if len(cell) != BytesPerCell {
			return &InvalidCellSizeError{Index: i, Got: len(cell), Want: BytesPerCell}
		}
	}

	// Validate KZG proofs count and sizes.
	if len(dc.KZGProofs) != len(dc.Cells) {
		return ErrProofCountMismatch
	}
	for _, proof := range dc.KZGProofs {
		if len(proof) != KZGProofLength {
			return ErrInvalidKZGProofLength
		}
	}

	// Validate commitments count and sizes.
	if len(dc.Commitments) != len(dc.Cells) {
		return ErrCommitmentCountMismatch
	}
	for _, comm := range dc.Commitments {
		if len(comm) != KZGCommitmentLength {
			return ErrInvalidCommitmentLength
		}
	}

	return nil
}
