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

package hotstuff

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestBatchSigningMessage(t *testing.T) {
	view := ViewNumber(42)
	h1 := types.Hash{0x01}
	h2 := types.Hash{0x02}
	hashes := []types.Hash{h1, h2}

	msg := BatchSigningMessage(view, hashes)

	// Expected size: 5 + 8 + 4 + 2*32 = 81
	expectedLen := 5 + 8 + 4 + 2*32
	if len(msg) != expectedLen {
		t.Fatalf("expected message length %d, got %d", expectedLen, len(msg))
	}

	// Verify prefix.
	if !bytes.Equal(msg[:5], []byte("batch")) {
		t.Error("expected 'batch' prefix")
	}

	// Verify view.
	gotView := binary.LittleEndian.Uint64(msg[5:13])
	if gotView != 42 {
		t.Errorf("expected view 42, got %d", gotView)
	}

	// Verify count.
	gotCount := binary.LittleEndian.Uint32(msg[13:17])
	if gotCount != 2 {
		t.Errorf("expected count 2, got %d", gotCount)
	}

	// Verify hashes.
	if !bytes.Equal(msg[17:49], h1[:]) {
		t.Error("first hash mismatch")
	}
	if !bytes.Equal(msg[49:81], h2[:]) {
		t.Error("second hash mismatch")
	}

	// Deterministic: same input -> same output.
	msg2 := BatchSigningMessage(view, hashes)
	if !bytes.Equal(msg, msg2) {
		t.Error("expected deterministic output")
	}
}

func TestBatchSigningMessage_SingleBlock(t *testing.T) {
	view := ViewNumber(1)
	h := types.Hash{0xAA}
	msg := BatchSigningMessage(view, []types.Hash{h})

	// Expected size: 5 + 8 + 4 + 32 = 49
	if len(msg) != 49 {
		t.Fatalf("expected length 49, got %d", len(msg))
	}

	// Verify count is 1.
	gotCount := binary.LittleEndian.Uint32(msg[13:17])
	if gotCount != 1 {
		t.Errorf("expected count 1, got %d", gotCount)
	}

	// Verify hash is present.
	if !bytes.Equal(msg[17:49], h[:]) {
		t.Error("hash mismatch")
	}
}

func TestValidateBatch_EmptyHashes(t *testing.T) {
	bp := &BatchProposal{
		View:         1,
		BlockHashes:  nil,
		TxRootHashes: nil,
	}
	err := ValidateBatch(bp)
	if !errors.Is(err, ErrEmptyBatch) {
		t.Errorf("expected ErrEmptyBatch, got %v", err)
	}
}

func TestValidateBatch_Mismatched(t *testing.T) {
	bp := &BatchProposal{
		View:         1,
		BlockHashes:  []types.Hash{{0x01}, {0x02}},
		TxRootHashes: []types.Hash{{0x01}},
	}
	err := ValidateBatch(bp)
	if !errors.Is(err, ErrMismatchedRoots) {
		t.Errorf("expected ErrMismatchedRoots, got %v", err)
	}
}

func TestValidateBatch_TooLarge(t *testing.T) {
	hashes := make([]types.Hash, MaxBatchSize+1)
	roots := make([]types.Hash, MaxBatchSize+1)
	bp := &BatchProposal{
		View:         1,
		BlockHashes:  hashes,
		TxRootHashes: roots,
	}
	err := ValidateBatch(bp)
	if !errors.Is(err, ErrBatchTooLarge) {
		t.Errorf("expected ErrBatchTooLarge, got %v", err)
	}
}

func TestValidateBatch_Valid(t *testing.T) {
	bp := &BatchProposal{
		View:         10,
		BlockHashes:  []types.Hash{{0x01}, {0x02}, {0x03}},
		TxRootHashes: []types.Hash{{0x11}, {0x12}, {0x13}},
		Proposer:     0,
	}
	err := ValidateBatch(bp)
	if err != nil {
		t.Errorf("expected nil error for valid batch, got %v", err)
	}
}
