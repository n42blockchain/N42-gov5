// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// header_prover_cov_test.go covers EncodePublicInputs/DecodePublicInputs
// round-tripping, fetchHeadersAndQCs error paths, and ProveHeaderRange using
// the scriptedChain fake already defined in publisher_test.go — no real
// network or database.

package bridge

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestEncodeDecodePublicInputs_RoundTrip(t *testing.T) {
	stateRoot := types.Hash{0x11, 0x22}
	encoded := EncodePublicInputs(5, 10, stateRoot)
	if len(encoded) != 48 {
		t.Fatalf("encoded length = %d, want 48", len(encoded))
	}

	start, end, root, err := DecodePublicInputs(encoded)
	if err != nil {
		t.Fatalf("DecodePublicInputs: %v", err)
	}
	if start != 5 || end != 10 || root != stateRoot {
		t.Fatalf("decoded = (%d, %d, %x), want (5, 10, %x)", start, end, root, stateRoot)
	}
}

func TestDecodePublicInputs_TooShort(t *testing.T) {
	if _, _, _, err := DecodePublicInputs([]byte{0x1, 0x2}); err == nil {
		t.Fatal("expected error for short input")
	}
}

func TestFetchHeadersAndQCs_InvalidRange(t *testing.T) {
	chain := newScriptedChain()
	chain.appendBlocks(5)

	if _, _, err := fetchHeadersAndQCs(chain, 5, 1); err == nil {
		t.Fatal("expected error for startBlock > endBlock")
	}
}

func TestFetchHeadersAndQCs_RangeTooLarge(t *testing.T) {
	chain := newScriptedChain()
	if _, _, err := fetchHeadersAndQCs(chain, 1, 20000); err == nil {
		t.Fatal("expected error for range exceeding maxHeaderRange")
	}
}

func TestFetchHeadersAndQCs_BlockNotFound(t *testing.T) {
	chain := newScriptedChain()
	chain.appendBlocks(2)

	if _, _, err := fetchHeadersAndQCs(chain, 1, 5); err == nil {
		t.Fatal("expected error when a block in range is missing")
	}
}

func TestFetchHeadersAndQCs_Success(t *testing.T) {
	chain := newScriptedChain()
	chain.appendBlocks(3)

	headers, qcs, err := fetchHeadersAndQCs(chain, 1, 3)
	if err != nil {
		t.Fatalf("fetchHeadersAndQCs: %v", err)
	}
	if len(headers) != 3 || len(qcs) != 3 {
		t.Fatalf("got %d headers, %d qcs, want 3 each", len(headers), len(qcs))
	}
}

func TestProveHeaderRange_Success(t *testing.T) {
	chain := newScriptedChain()
	chain.appendBlocks(4)

	proof, err := ProveHeaderRange(chain, nil, 1, 4)
	if err != nil {
		t.Fatalf("ProveHeaderRange: %v", err)
	}
	if proof.StartBlock != 1 || proof.EndBlock != 4 {
		t.Fatalf("proof range = [%d,%d], want [1,4]", proof.StartBlock, proof.EndBlock)
	}
	if proof.ProofData != nil {
		t.Fatal("ProveHeaderRange (local-only) should not set ProofData")
	}
}

func TestProveHeaderRange_FetchError(t *testing.T) {
	chain := newScriptedChain()
	if _, err := ProveHeaderRange(chain, nil, 1, 3); err == nil {
		t.Fatal("expected error when blocks are missing")
	}
}
