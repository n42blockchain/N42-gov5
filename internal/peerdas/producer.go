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
// ProduceColumns: turns a block's blob sidecars into NumberOfColumns
// (128) DataColumns as specified by EIP-7594. Each resulting column
// aggregates the i-th cell across every blob in the block together
// with the cell KZG proof and the source blob commitment, ready to be
// stored or gossiped to custody peers.

package peerdas

import (
	"fmt"

	"github.com/n42blockchain/N42/crypto/kzg"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// ProduceColumns converts a block's blob sidecars into NumberOfColumns (128)
// DataColumns. Each column i contains the i-th cell from each blob, along with
// the corresponding KZG proof and the blob's commitment.
//
// The caller provides the blobs and their commitments. The function computes
// cells and proofs using the KZG trusted setup and transposes them into columns.
func ProduceColumns(
	blobs []transaction.Blob,
	commitments []transaction.Commitment,
	blockHash types.Hash,
	blockNum uint64,
) ([NumberOfColumns]*DataColumn, error) {
	var result [NumberOfColumns]*DataColumn

	nBlobs := len(blobs)
	if nBlobs == 0 {
		return result, ErrNoBlobsProvided
	}
	if nBlobs != len(commitments) {
		return result, ErrCommitmentCountMismatch
	}

	ctx, err := kzg.GetPeerDASContext()
	if err != nil {
		return result, fmt.Errorf("peerdas: init KZG context: %w", err)
	}

	// Compute cells and proofs for each blob.
	// allCells[blobIdx][colIdx] = *Cell
	// allProofs[blobIdx][colIdx] = Proof
	type blobResult struct {
		cells  [kzg.CellsPerExtBlob]*kzg.Cell
		proofs [kzg.CellsPerExtBlob]transaction.Proof
	}
	blobResults := make([]blobResult, nBlobs)

	for i := 0; i < nBlobs; i++ {
		blob := blobs[i]
		cells, proofs, err := ctx.BlobToCellsAndProofs((*transaction.Blob)(&blob))
		if err != nil {
			return result, fmt.Errorf("peerdas: compute cells for blob %d: %w", i, err)
		}
		blobResults[i] = blobResult{cells: cells, proofs: proofs}
	}

	// Transpose: for each column index, collect the cell from each blob.
	for colIdx := uint64(0); colIdx < NumberOfColumns; colIdx++ {
		cells := make([][]byte, nBlobs)
		kzgProofs := make([][]byte, nBlobs)
		comms := make([][]byte, nBlobs)

		for blobIdx := 0; blobIdx < nBlobs; blobIdx++ {
			// Cell data.
			cellData := make([]byte, BytesPerCell)
			copy(cellData, blobResults[blobIdx].cells[colIdx][:])
			cells[blobIdx] = cellData

			// Proof.
			proof := make([]byte, KZGProofLength)
			copy(proof, blobResults[blobIdx].proofs[colIdx][:])
			kzgProofs[blobIdx] = proof

			// Commitment.
			comm := make([]byte, KZGCommitmentLength)
			copy(comm, commitments[blobIdx][:])
			comms[blobIdx] = comm
		}

		result[colIdx] = &DataColumn{
			Index:       colIdx,
			BlockHash:   blockHash,
			BlockNumber: blockNum,
			Cells:       cells,
			KZGProofs:   kzgProofs,
			Commitments: comms,
		}
	}

	return result, nil
}
