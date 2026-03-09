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
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func newTestDB(t *testing.T) kv.RwDB {
	t.Helper()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	return memdb.NewTestDB(t)
}

func makeBlobSidecar(index uint64, blockNum uint64, blockHash types.Hash) *block.BlobSidecar {
	sc := &block.BlobSidecar{
		Index:       index,
		BlockNumber: blockNum,
		BlockHash:   blockHash,
		TxHash:      types.HexToHash("0xdeadbeef"),
	}
	// Fill blob with some pattern.
	for i := range sc.Blob {
		sc.Blob[i] = byte(index)
	}
	// Fill KZG commitment.
	for i := range sc.KZGCommitment {
		sc.KZGCommitment[i] = byte(index + 1)
	}
	// Fill KZG proof.
	for i := range sc.KZGProof {
		sc.KZGProof[i] = byte(index + 2)
	}
	// Add some inclusion proof entries.
	sc.CommitmentInclusionProof = make([][32]byte, 3)
	for i := range sc.CommitmentInclusionProof {
		sc.CommitmentInclusionProof[i][0] = byte(i)
	}
	return sc
}

func TestBlobSidecarStorage(t *testing.T) {
	db := newTestDB(t)
	blockNum := uint64(100)
	blockHash := types.HexToHash("0xaabbccdd")

	sidecars := []*block.BlobSidecar{
		makeBlobSidecar(0, blockNum, blockHash),
		makeBlobSidecar(1, blockNum, blockHash),
		makeBlobSidecar(2, blockNum, blockHash),
	}

	// Write.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return WriteBlobSidecars(tx, blockNum, blockHash, sidecars)
	}); err != nil {
		t.Fatal("WriteBlobSidecars failed:", err)
	}

	// Read back.
	var readSidecars []*block.BlobSidecar
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		readSidecars, err = ReadBlobSidecars(tx, blockNum, blockHash)
		return err
	}); err != nil {
		t.Fatal("ReadBlobSidecars failed:", err)
	}

	if len(readSidecars) != 3 {
		t.Fatalf("expected 3 sidecars, got %d", len(readSidecars))
	}

	for i, sc := range readSidecars {
		if sc.Index != uint64(i) {
			t.Errorf("sidecar[%d].Index = %d, want %d", i, sc.Index, i)
		}
		if sc.BlockNumber != blockNum {
			t.Errorf("sidecar[%d].BlockNumber = %d, want %d", i, sc.BlockNumber, blockNum)
		}
		if sc.BlockHash != blockHash {
			t.Errorf("sidecar[%d].BlockHash = %s, want %s", i, sc.BlockHash, blockHash)
		}
		// Verify blob pattern.
		for j := range sc.Blob {
			if sc.Blob[j] != byte(i) {
				t.Errorf("sidecar[%d].Blob[%d] = %d, want %d", i, j, sc.Blob[j], byte(i))
				break
			}
		}
		// Verify KZG commitment.
		for j := range sc.KZGCommitment {
			if sc.KZGCommitment[j] != byte(i+1) {
				t.Errorf("sidecar[%d].KZGCommitment[%d] = %d, want %d", i, j, sc.KZGCommitment[j], byte(i+1))
				break
			}
		}
		// Verify inclusion proof.
		if len(sc.CommitmentInclusionProof) != 3 {
			t.Errorf("sidecar[%d] proof len = %d, want 3", i, len(sc.CommitmentInclusionProof))
		}
	}

	// Delete.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return DeleteBlobSidecars(tx, blockNum, blockHash)
	}); err != nil {
		t.Fatal("DeleteBlobSidecars failed:", err)
	}

	// Confirm deleted.
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		result, err := ReadBlobSidecars(tx, blockNum, blockHash)
		if err != nil {
			return err
		}
		if result != nil {
			t.Errorf("expected nil after delete, got %d sidecars", len(result))
		}
		return nil
	}); err != nil {
		t.Fatal("ReadBlobSidecars after delete failed:", err)
	}
}

func TestBlobSidecarValidation(t *testing.T) {
	// nil sidecar
	var nilSc *block.BlobSidecar
	if err := nilSc.Validate(); err == nil {
		t.Error("expected error for nil sidecar")
	}

	// index too high
	sc := &block.BlobSidecar{Index: 10, BlockHash: types.HexToHash("0x01")}
	if err := sc.Validate(); err == nil {
		t.Error("expected error for index >= MaxBlobsPerBlock")
	}

	// empty block hash
	sc = &block.BlobSidecar{Index: 0}
	if err := sc.Validate(); err == nil {
		t.Error("expected error for empty block hash")
	}

	// valid sidecar
	sc = &block.BlobSidecar{Index: 0, BlockHash: types.HexToHash("0x01")}
	if err := sc.Validate(); err != nil {
		t.Errorf("expected no error for valid sidecar, got %v", err)
	}
}

func TestBlobSidecarEncoding(t *testing.T) {
	blockNum := uint64(42)
	blockHash := types.HexToHash("0x1234567890")

	original := []*block.BlobSidecar{
		makeBlobSidecar(0, blockNum, blockHash),
		makeBlobSidecar(3, blockNum, blockHash),
	}

	data, err := encodeBlobSidecars(original)
	if err != nil {
		t.Fatal("encodeBlobSidecars failed:", err)
	}

	decoded, err := decodeBlobSidecars(data)
	if err != nil {
		t.Fatal("decodeBlobSidecars failed:", err)
	}

	if len(decoded) != len(original) {
		t.Fatalf("decoded len = %d, want %d", len(decoded), len(original))
	}

	for i := range decoded {
		if decoded[i].Index != original[i].Index {
			t.Errorf("decoded[%d].Index = %d, want %d", i, decoded[i].Index, original[i].Index)
		}
		if decoded[i].BlockNumber != original[i].BlockNumber {
			t.Errorf("decoded[%d].BlockNumber = %d, want %d", i, decoded[i].BlockNumber, original[i].BlockNumber)
		}
		if decoded[i].BlockHash != original[i].BlockHash {
			t.Errorf("decoded[%d].BlockHash mismatch", i)
		}
		if decoded[i].TxHash != original[i].TxHash {
			t.Errorf("decoded[%d].TxHash mismatch", i)
		}
		if decoded[i].Blob != original[i].Blob {
			t.Errorf("decoded[%d].Blob mismatch", i)
		}
		if decoded[i].KZGCommitment != original[i].KZGCommitment {
			t.Errorf("decoded[%d].KZGCommitment mismatch", i)
		}
		if decoded[i].KZGProof != original[i].KZGProof {
			t.Errorf("decoded[%d].KZGProof mismatch", i)
		}
		if len(decoded[i].CommitmentInclusionProof) != len(original[i].CommitmentInclusionProof) {
			t.Errorf("decoded[%d].CommitmentInclusionProof len = %d, want %d",
				i, len(decoded[i].CommitmentInclusionProof), len(original[i].CommitmentInclusionProof))
		}
	}
}

func TestBlobSidecarEmptyWrite(t *testing.T) {
	db := newTestDB(t)
	blockNum := uint64(1)
	blockHash := types.HexToHash("0x01")

	// Writing empty sidecars should be a no-op.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return WriteBlobSidecars(tx, blockNum, blockHash, nil)
	}); err != nil {
		t.Fatal("WriteBlobSidecars with nil should not fail:", err)
	}

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return WriteBlobSidecars(tx, blockNum, blockHash, []*block.BlobSidecar{})
	}); err != nil {
		t.Fatal("WriteBlobSidecars with empty slice should not fail:", err)
	}
}

func TestBlobSidecarDecodeTruncated(t *testing.T) {
	// Too short.
	_, err := decodeBlobSidecars([]byte{0x01, 0x02})
	if err == nil {
		t.Error("expected error for truncated data")
	}

	// Count but no data.
	_, err = decodeBlobSidecars([]byte{0x01, 0x00, 0x00, 0x00})
	if err == nil {
		t.Error("expected error for count with no data")
	}
}
