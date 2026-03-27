// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package kzg

import (
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
)

func TestPeerDASContext_Init(t *testing.T) {
	ctx, err := GetPeerDASContext()
	if err != nil {
		t.Fatalf("GetPeerDASContext: %v", err)
	}
	if ctx == nil {
		t.Fatal("context should not be nil")
	}

	// Second call returns the same instance.
	ctx2, err := GetPeerDASContext()
	if err != nil {
		t.Fatalf("GetPeerDASContext 2nd call: %v", err)
	}
	if ctx != ctx2 {
		t.Error("expected same context instance")
	}
}

func TestPeerDAS_BlobToCellsAndProofs(t *testing.T) {
	ctx, err := GetPeerDASContext()
	if err != nil {
		t.Fatalf("GetPeerDASContext: %v", err)
	}

	// Zero blob is a valid blob (all field elements are zero, which is canonical).
	var blob transaction.Blob
	cells, proofs, err := ctx.BlobToCellsAndProofs(&blob)
	if err != nil {
		t.Fatalf("BlobToCellsAndProofs: %v", err)
	}

	// Should produce CellsPerExtBlob cells.
	for i := 0; i < CellsPerExtBlob; i++ {
		if cells[i] == nil {
			t.Fatalf("cell %d is nil", i)
		}
		if len(cells[i]) != BytesPerCell {
			t.Errorf("cell %d size: got %d, want %d", i, len(cells[i]), BytesPerCell)
		}
	}

	// Each proof should be 48 bytes.
	for i := 0; i < CellsPerExtBlob; i++ {
		if len(proofs[i]) != BytesPerProof {
			t.Errorf("proof %d size: got %d, want %d", i, len(proofs[i]), BytesPerProof)
		}
	}
}

func TestPeerDAS_VerifyCellKZGProofBatch_Valid(t *testing.T) {
	ctx, err := GetPeerDASContext()
	if err != nil {
		t.Fatalf("GetPeerDASContext: %v", err)
	}

	var blob transaction.Blob
	cells, proofs, err := ctx.BlobToCellsAndProofs(&blob)
	if err != nil {
		t.Fatalf("BlobToCellsAndProofs: %v", err)
	}

	commitment, err := BlobToCommitment(&blob)
	if err != nil {
		t.Fatalf("BlobToCommitment: %v", err)
	}

	// Verify a batch of cells (first 4).
	batchSize := 4
	commitments := make([]Commitment, batchSize)
	cellIndices := make([]uint64, batchSize)
	batchCells := make([]*Cell, batchSize)
	batchProofs := make([]Proof, batchSize)

	for i := 0; i < batchSize; i++ {
		commitments[i] = commitment
		cellIndices[i] = uint64(i)
		batchCells[i] = cells[i]
		batchProofs[i] = proofs[i]
	}

	if err := ctx.VerifyCellKZGProofBatch(commitments, cellIndices, batchCells, batchProofs); err != nil {
		t.Fatalf("VerifyCellKZGProofBatch: %v", err)
	}
}

func TestPeerDAS_VerifyCellKZGProofBatch_Invalid(t *testing.T) {
	ctx, err := GetPeerDASContext()
	if err != nil {
		t.Fatalf("GetPeerDASContext: %v", err)
	}

	var blob transaction.Blob
	cells, proofs, err := ctx.BlobToCellsAndProofs(&blob)
	if err != nil {
		t.Fatalf("BlobToCellsAndProofs: %v", err)
	}

	commitment, err := BlobToCommitment(&blob)
	if err != nil {
		t.Fatalf("BlobToCommitment: %v", err)
	}

	// Tamper with the first cell.
	tamperedCell := new(Cell)
	copy(tamperedCell[:], cells[0][:])
	tamperedCell[100] ^= 0xff

	commitments := []Commitment{commitment}
	cellIndices := []uint64{0}
	batchCells := []*Cell{tamperedCell}
	batchProofs := []Proof{proofs[0]}

	if err := ctx.VerifyCellKZGProofBatch(commitments, cellIndices, batchCells, batchProofs); err == nil {
		t.Fatal("expected verification to fail with tampered cell")
	}
}

func TestPeerDAS_RecoverCellsAndProofs(t *testing.T) {
	ctx, err := GetPeerDASContext()
	if err != nil {
		t.Fatalf("GetPeerDASContext: %v", err)
	}

	var blob transaction.Blob
	allCells, allProofs, err := ctx.BlobToCellsAndProofs(&blob)
	if err != nil {
		t.Fatalf("BlobToCellsAndProofs: %v", err)
	}

	// Provide only even-indexed cells (64 out of 128 = 50%).
	var cellIDs []uint64
	var partialCells []*Cell
	for i := uint64(0); i < CellsPerExtBlob; i += 2 {
		cellIDs = append(cellIDs, i)
		partialCells = append(partialCells, allCells[i])
	}

	recoveredCells, recoveredProofs, err := ctx.RecoverCellsAndProofs(cellIDs, partialCells)
	if err != nil {
		t.Fatalf("RecoverCellsAndProofs: %v", err)
	}

	// Recovered cells should match original cells.
	for i := 0; i < CellsPerExtBlob; i++ {
		if recoveredCells[i] == nil {
			t.Fatalf("recovered cell %d is nil", i)
		}
		if *recoveredCells[i] != *allCells[i] {
			t.Errorf("recovered cell %d does not match original", i)
		}
	}

	// Recovered proofs should match original proofs.
	for i := 0; i < CellsPerExtBlob; i++ {
		if recoveredProofs[i] != allProofs[i] {
			t.Errorf("recovered proof %d does not match original", i)
		}
	}
}

func TestPeerDAS_VerifyCellKZGProofBatch_EmptyBatch(t *testing.T) {
	ctx, err := GetPeerDASContext()
	if err != nil {
		t.Fatalf("GetPeerDASContext: %v", err)
	}

	// Empty batch should succeed.
	err = ctx.VerifyCellKZGProofBatch(nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("empty batch should succeed: %v", err)
	}
}

func TestPeerDAS_Constants(t *testing.T) {
	if CellsPerExtBlob != 128 {
		t.Errorf("CellsPerExtBlob: got %d, want 128", CellsPerExtBlob)
	}
	if BytesPerCell != 2048 {
		t.Errorf("BytesPerCell: got %d, want 2048", BytesPerCell)
	}
}

func BenchmarkBlobToCellsAndProofs(b *testing.B) {
	ctx, err := GetPeerDASContext()
	if err != nil {
		b.Fatalf("GetPeerDASContext: %v", err)
	}

	var blob transaction.Blob
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := ctx.BlobToCellsAndProofs(&blob)
		if err != nil {
			b.Fatalf("BlobToCellsAndProofs: %v", err)
		}
	}
}

func BenchmarkVerifyCellKZGProofBatch(b *testing.B) {
	ctx, err := GetPeerDASContext()
	if err != nil {
		b.Fatalf("GetPeerDASContext: %v", err)
	}

	var blob transaction.Blob
	cells, proofs, err := ctx.BlobToCellsAndProofs(&blob)
	if err != nil {
		b.Fatalf("BlobToCellsAndProofs: %v", err)
	}

	commitment, cErr := BlobToCommitment(&blob)
	if cErr != nil {
		b.Fatalf("BlobToCommitment: %v", cErr)
	}

	// Batch of 4 cells.
	commitments := make([]Commitment, 4)
	cellIndices := make([]uint64, 4)
	batchCells := make([]*Cell, 4)
	batchProofs := make([]Proof, 4)
	for i := 0; i < 4; i++ {
		commitments[i] = commitment
		cellIndices[i] = uint64(i)
		batchCells[i] = cells[i]
		batchProofs[i] = proofs[i]
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := ctx.VerifyCellKZGProofBatch(commitments, cellIndices, batchCells, batchProofs); err != nil {
			b.Fatalf("VerifyCellKZGProofBatch: %v", err)
		}
	}
}
