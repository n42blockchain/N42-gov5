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
	"fmt"

	"github.com/n42blockchain/N42/common/crypto/kzg"
	"github.com/n42blockchain/N42/common/transaction"
)

// Verifier performs cryptographic KZG verification on data columns.
type Verifier struct {
	ctx *kzg.PeerDASContext
}

// NewVerifier creates a KZG verifier, initializing the PeerDAS context.
// This takes 2-5 seconds on first call due to trusted setup processing.
func NewVerifier() (*Verifier, error) {
	ctx, err := kzg.GetPeerDASContext()
	if err != nil {
		return nil, fmt.Errorf("peerdas: init KZG context: %w", err)
	}
	return &Verifier{ctx: ctx}, nil
}

// VerifyDataColumn performs cryptographic KZG cell proof verification on a DataColumn.
// For each blob in the column, it verifies that the cell at the column's index
// is consistent with the blob's commitment using the provided KZG proof.
//
// Uses batch verification (VerifyCellKZGProofBatch) for efficiency.
func (v *Verifier) VerifyDataColumn(col *DataColumn) error {
	if col == nil {
		return ErrNilColumn
	}

	blobCount := len(col.Cells)
	if blobCount == 0 {
		return ErrEmptyColumnData
	}
	if len(col.KZGProofs) != blobCount {
		return ErrProofCountMismatch
	}
	if len(col.Commitments) != blobCount {
		return ErrCommitmentCountMismatch
	}

	// Build parallel arrays for batch verification.
	commitments := make([]transaction.Commitment, blobCount)
	cellIndices := make([]uint64, blobCount)
	cells := make([]*kzg.Cell, blobCount)
	proofs := make([]transaction.Proof, blobCount)

	for i := 0; i < blobCount; i++ {
		// Commitment.
		if len(col.Commitments[i]) != KZGCommitmentLength {
			return ErrInvalidCommitmentLength
		}
		copy(commitments[i][:], col.Commitments[i])

		// Cell index is the column index for all blobs.
		cellIndices[i] = col.Index

		// Cell data: must be exactly BytesPerCell.
		if len(col.Cells[i]) != BytesPerCell {
			return &InvalidCellSizeError{Index: i, Got: len(col.Cells[i]), Want: BytesPerCell}
		}
		cell := new(kzg.Cell)
		copy(cell[:], col.Cells[i])
		cells[i] = cell

		// Proof.
		if len(col.KZGProofs[i]) != KZGProofLength {
			return ErrInvalidKZGProofLength
		}
		copy(proofs[i][:], col.KZGProofs[i])
	}

	if err := v.ctx.VerifyCellKZGProofBatch(commitments, cellIndices, cells, proofs); err != nil {
		return fmt.Errorf("%w: %v", ErrKZGVerificationFailed, err)
	}
	return nil
}

// VerifyDataColumnLight performs structural validation only (no cryptographic check).
// Used as a fast pre-filter before full KZG verification.
func VerifyDataColumnLight(col *DataColumn) error {
	if col == nil {
		return ErrNilColumn
	}
	if col.Index >= NumberOfColumns {
		return ErrColumnIndexOutOfRange
	}
	if len(col.Cells) == 0 {
		return ErrEmptyColumnData
	}
	if len(col.KZGProofs) != len(col.Cells) {
		return ErrProofCountMismatch
	}
	if len(col.Commitments) != len(col.Cells) {
		return ErrCommitmentCountMismatch
	}
	return nil
}
