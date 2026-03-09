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

package sync_pb

import (
	"bytes"
	"testing"
)

func TestBlobSidecarsByRangeRequestSSZ(t *testing.T) {
	orig := &BlobSidecarsByRangeRequest{
		StartBlock: 100,
		Count:      10,
	}

	data, err := orig.MarshalSSZ()
	if err != nil {
		t.Fatal("MarshalSSZ failed:", err)
	}

	decoded := &BlobSidecarsByRangeRequest{}
	if err := decoded.UnmarshalSSZ(data); err != nil {
		t.Fatal("UnmarshalSSZ failed:", err)
	}

	if decoded.StartBlock != orig.StartBlock {
		t.Errorf("StartBlock = %d, want %d", decoded.StartBlock, orig.StartBlock)
	}
	if decoded.Count != orig.Count {
		t.Errorf("Count = %d, want %d", decoded.Count, orig.Count)
	}
	if orig.SizeSSZ() != 16 {
		t.Errorf("SizeSSZ = %d, want 16", orig.SizeSSZ())
	}
}

func TestBlobSidecarsByRootRequestSSZ(t *testing.T) {
	blockRoot := make([]byte, 32)
	blockRoot[0] = 0xAA

	orig := &BlobSidecarsByRootRequest{
		Identifiers: []*BlobIdentifierMsg{
			{BlockRoot: blockRoot, Index: 0},
			{BlockRoot: blockRoot, Index: 3},
		},
	}

	data, err := orig.MarshalSSZ()
	if err != nil {
		t.Fatal("MarshalSSZ failed:", err)
	}

	decoded := &BlobSidecarsByRootRequest{}
	if err := decoded.UnmarshalSSZ(data); err != nil {
		t.Fatal("UnmarshalSSZ failed:", err)
	}

	if len(decoded.Identifiers) != 2 {
		t.Fatalf("Identifiers len = %d, want 2", len(decoded.Identifiers))
	}
	if !bytes.Equal(decoded.Identifiers[0].BlockRoot, blockRoot) {
		t.Error("BlockRoot mismatch")
	}
	if decoded.Identifiers[1].Index != 3 {
		t.Errorf("Index = %d, want 3", decoded.Identifiers[1].Index)
	}
}

func TestBlobSidecarMsgSSZ(t *testing.T) {
	orig := &BlobSidecarMsg{
		Index:         2,
		Blob:          make([]byte, 131072),
		KZGCommitment: make([]byte, 48),
		KZGProof:      make([]byte, 48),
		BlockNumber:   42,
		BlockHash:     make([]byte, 32),
		TxHash:        make([]byte, 32),
	}
	orig.Blob[0] = 0xFF
	orig.KZGCommitment[0] = 0xAA
	orig.KZGProof[0] = 0xBB
	orig.BlockHash[0] = 0xCC
	orig.TxHash[0] = 0xDD

	data, err := orig.MarshalSSZ()
	if err != nil {
		t.Fatal("MarshalSSZ failed:", err)
	}

	decoded := &BlobSidecarMsg{}
	if err := decoded.UnmarshalSSZ(data); err != nil {
		t.Fatal("UnmarshalSSZ failed:", err)
	}

	if decoded.Index != 2 {
		t.Errorf("Index = %d, want 2", decoded.Index)
	}
	if decoded.BlockNumber != 42 {
		t.Errorf("BlockNumber = %d, want 42", decoded.BlockNumber)
	}
	if decoded.Blob[0] != 0xFF {
		t.Errorf("Blob[0] = %d, want 0xFF", decoded.Blob[0])
	}
	if decoded.KZGCommitment[0] != 0xAA {
		t.Errorf("KZGCommitment[0] = %d, want 0xAA", decoded.KZGCommitment[0])
	}
	if decoded.KZGProof[0] != 0xBB {
		t.Errorf("KZGProof[0] = %d, want 0xBB", decoded.KZGProof[0])
	}
	if decoded.BlockHash[0] != 0xCC {
		t.Errorf("BlockHash[0] = %d, want 0xCC", decoded.BlockHash[0])
	}
	if decoded.TxHash[0] != 0xDD {
		t.Errorf("TxHash[0] = %d, want 0xDD", decoded.TxHash[0])
	}
}

func TestBlobSidecarsResponseSSZ(t *testing.T) {
	orig := &BlobSidecarsResponse{
		Sidecars: []*BlobSidecarMsg{
			{
				Index:         0,
				Blob:          make([]byte, 131072),
				KZGCommitment: make([]byte, 48),
				KZGProof:      make([]byte, 48),
				BlockNumber:   1,
				BlockHash:     make([]byte, 32),
				TxHash:        make([]byte, 32),
			},
			{
				Index:         1,
				Blob:          make([]byte, 131072),
				KZGCommitment: make([]byte, 48),
				KZGProof:      make([]byte, 48),
				BlockNumber:   1,
				BlockHash:     make([]byte, 32),
				TxHash:        make([]byte, 32),
			},
		},
	}

	data, err := orig.MarshalSSZ()
	if err != nil {
		t.Fatal("MarshalSSZ failed:", err)
	}

	decoded := &BlobSidecarsResponse{}
	if err := decoded.UnmarshalSSZ(data); err != nil {
		t.Fatal("UnmarshalSSZ failed:", err)
	}

	if len(decoded.Sidecars) != 2 {
		t.Fatalf("Sidecars len = %d, want 2", len(decoded.Sidecars))
	}
	if decoded.Sidecars[0].Index != 0 {
		t.Errorf("Sidecars[0].Index = %d, want 0", decoded.Sidecars[0].Index)
	}
	if decoded.Sidecars[1].Index != 1 {
		t.Errorf("Sidecars[1].Index = %d, want 1", decoded.Sidecars[1].Index)
	}
}
