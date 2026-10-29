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
// EIP-7594 PeerDAS cell helpers built on go-eth-kzg.
// Declares CellsPerExtBlob (128), BytesPerCell (2048) and a Cell
// alias over goethkzg.Cell, plus the process-wide PeerDAS context
// lazily initialized under sync.Once. Provides the column sampling
// primitives consumed by internal/peerdas.

package kzg

import (
	"errors"
	"runtime"
	"sync"

	goethkzg "github.com/crate-crypto/go-eth-kzg"
)

// EIP-7594 PeerDAS constants.
const (
	// CellsPerExtBlob is the number of cells in an extended blob (EIP-7594).
	CellsPerExtBlob = goethkzg.CellsPerExtBlob // 128

	// BytesPerCell is the size of a single cell in bytes (64 field elements * 32 bytes).
	BytesPerCell = goethkzg.BytesPerCell // 2048
)

// Cell is a fixed-size byte array representing a single cell of an extended blob.
type Cell = goethkzg.Cell

// Global PeerDAS context (initialized once).
var (
	gPeerDASCtx     *PeerDASContext
	gPeerDASCtxOnce sync.Once
	gPeerDASCtxErr  error
)

// PeerDAS-specific errors.
var (
	ErrPeerDASContextNotInitialized = errors.New("kzg: peerdas context not initialized")
	ErrCellCountMismatch            = errors.New("kzg: cell count mismatch")
	ErrCellIndexOutOfRange          = errors.New("kzg: cell index out of range")
)

// PeerDASContext wraps go-eth-kzg Context for EIP-7594 cell operations.
type PeerDASContext struct {
	inner *goethkzg.Context
}

// InitPeerDASContext initializes the global PeerDAS KZG context.
// The context uses the same Ethereum trusted setup as the EIP-4844 context.
func InitPeerDASContext() error {
	gPeerDASCtxOnce.Do(func() {
		inner, err := goethkzg.NewContext4096Secure()
		if err != nil {
			gPeerDASCtxErr = err
			return
		}
		gPeerDASCtx = &PeerDASContext{inner: inner}
	})
	return gPeerDASCtxErr
}

// GetPeerDASContext returns the global PeerDAS KZG context, initializing it if needed.
func GetPeerDASContext() (*PeerDASContext, error) {
	if err := InitPeerDASContext(); err != nil {
		return nil, err
	}
	if gPeerDASCtx == nil {
		return nil, ErrPeerDASContextNotInitialized
	}
	return gPeerDASCtx, nil
}

// BlobToCellsAndProofs computes all 128 cells and their KZG proofs for a blob.
// Each cell is BytesPerCell (2048) bytes and each proof is 48 bytes.
func (c *PeerDASContext) BlobToCellsAndProofs(blob *Blob) ([CellsPerExtBlob]*Cell, [CellsPerExtBlob]Proof, error) {
	gBlob := (*goethkzg.Blob)(blob)
	cells, kzgProofs, err := c.inner.ComputeCellsAndKZGProofs(gBlob, runtime.NumCPU())
	if err != nil {
		return [CellsPerExtBlob]*Cell{}, [CellsPerExtBlob]Proof{}, err
	}
	var proofs [CellsPerExtBlob]Proof
	for i := range kzgProofs {
		copy(proofs[i][:], kzgProofs[i][:])
	}
	return cells, proofs, nil
}

// VerifyCellKZGProofBatch verifies multiple cell KZG proofs in batch.
// Parameters correspond to parallel arrays: for each entry i,
// commitments[i] is the blob commitment, cellIndices[i] is the column index,
// cells[i] is the cell data, and proofs[i] is the KZG proof.
func (c *PeerDASContext) VerifyCellKZGProofBatch(
	commitments []Commitment,
	cellIndices []uint64,
	cells []*Cell,
	proofs []Proof,
) error {
	n := len(commitments)
	if n != len(cellIndices) || n != len(cells) || n != len(proofs) {
		return ErrInputLengthMismatch
	}

	gCommitments := make([]goethkzg.KZGCommitment, n)
	gProofs := make([]goethkzg.KZGProof, n)
	for i := 0; i < n; i++ {
		copy(gCommitments[i][:], commitments[i][:])
		copy(gProofs[i][:], proofs[i][:])
	}

	return c.inner.VerifyCellKZGProofBatch(gCommitments, cellIndices, cells, gProofs)
}

// ComputeCellProofs computes the CellsPerExtBlob cell KZG proofs of a blob,
// in cell-index order, as an EIP-7594 version-1 sidecar carries them.
func ComputeCellProofs(blob *Blob) ([]Proof, error) {
	ctx, err := GetPeerDASContext()
	if err != nil {
		return nil, err
	}
	_, kzgProofs, err := ctx.inner.ComputeCellsAndKZGProofs((*goethkzg.Blob)(blob), 0)
	if err != nil {
		return nil, err
	}
	proofs := make([]Proof, len(kzgProofs))
	for i := range kzgProofs {
		proofs[i] = Proof(kzgProofs[i])
	}
	return proofs, nil
}

// VerifyCellProofs verifies the cell proofs of whole blobs in one batch:
// proofs holds CellsPerExtBlob proofs per blob, blob after blob, as in a
// version-1 sidecar. The cells are recomputed from the blobs, so a valid
// batch proves every blob matches its commitment (geth's VerifyCellProofs).
func VerifyCellProofs(blobs []Blob, commitments []Commitment, proofs []Proof) error {
	if len(blobs) != len(commitments) || len(proofs) != len(blobs)*CellsPerExtBlob {
		return ErrInputLengthMismatch
	}
	ctx, err := GetPeerDASContext()
	if err != nil {
		return err
	}
	n := len(proofs)
	var (
		gCommitments = make([]goethkzg.KZGCommitment, 0, n)
		gProofs      = make([]goethkzg.KZGProof, n)
		cellIndices  = make([]uint64, 0, n)
		cells        = make([]*Cell, 0, n)
	)
	for i := range proofs {
		gProofs[i] = goethkzg.KZGProof(proofs[i])
	}
	for i := range blobs {
		blobCells, err := ctx.inner.ComputeCells((*goethkzg.Blob)(&blobs[i]), 0)
		if err != nil {
			return err
		}
		for idx := range blobCells {
			gCommitments = append(gCommitments, goethkzg.KZGCommitment(commitments[i]))
			cellIndices = append(cellIndices, uint64(idx))
			cells = append(cells, blobCells[idx])
		}
	}
	return ctx.inner.VerifyCellKZGProofBatch(gCommitments, cellIndices, cells, gProofs)
}

// RecoverCellsAndProofs reconstructs all 128 cells and proofs from a subset.
// Requires at least 64 cells (50% of 128) for successful reconstruction.
// cellIDs must be sorted in ascending order.
func (c *PeerDASContext) RecoverCellsAndProofs(
	cellIDs []uint64,
	cells []*Cell,
) ([CellsPerExtBlob]*Cell, [CellsPerExtBlob]Proof, error) {
	allCells, kzgProofs, err := c.inner.RecoverCellsAndComputeKZGProofs(cellIDs, cells, runtime.NumCPU())
	if err != nil {
		return [CellsPerExtBlob]*Cell{}, [CellsPerExtBlob]Proof{}, err
	}
	var proofs [CellsPerExtBlob]Proof
	for i := range kzgProofs {
		copy(proofs[i][:], kzgProofs[i][:])
	}
	return allCells, proofs, nil
}
