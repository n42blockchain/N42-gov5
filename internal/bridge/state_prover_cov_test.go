// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// state_prover_cov_test.go exercises ProveStateInclusion / VerifyStateInclusion
// / EncodeStateProof against an in-memory JMT tree (lib/jmt MemStore) — no
// real network or database involved.

package bridge

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/jmt"
)

func newCovTestJMTTree(t *testing.T, key []byte, value []byte) (*jmt.Tree, types.Hash) {
	t.Helper()
	store := jmt.NewMemStore()
	tree := jmt.New(store)

	keyHash := jmt.HashKey(key)
	root, err := tree.BatchUpdate([]jmt.BatchEntry{{KeyHash: keyHash, Value: value}})
	if err != nil {
		t.Fatalf("BatchUpdate: %v", err)
	}
	return tree, types.Hash(root)
}

func TestProveStateInclusion_NilTree(t *testing.T) {
	if _, err := ProveStateInclusion(nil, types.Hash{}, []byte("key")); err == nil {
		t.Fatal("expected error for nil tree")
	}
}

func TestProveStateInclusion_RoundTrip(t *testing.T) {
	key := []byte("account-1")
	value := []byte{0x42}
	tree, root := newCovTestJMTTree(t, key, value)

	proof, err := ProveStateInclusion(tree, root, key)
	if err != nil {
		t.Fatalf("ProveStateInclusion: %v", err)
	}
	if proof.StateRoot != root {
		t.Fatalf("StateRoot = %x, want %x", proof.StateRoot, root)
	}
	if string(proof.Value) != string(value) {
		t.Fatalf("Value = %x, want %x", proof.Value, value)
	}

	got, err := VerifyStateInclusion(root, proof)
	if err != nil {
		t.Fatalf("VerifyStateInclusion: %v", err)
	}
	if string(got) != string(value) {
		t.Fatalf("verified value = %x, want %x", got, value)
	}
}

func TestVerifyStateInclusion_NilProof(t *testing.T) {
	if _, err := VerifyStateInclusion(types.Hash{}, nil); err == nil {
		t.Fatal("expected error for nil proof")
	}
	if _, err := VerifyStateInclusion(types.Hash{}, &StateInclusionProof{}); err == nil {
		t.Fatal("expected error for proof with nil JMTProof")
	}
}

func TestEncodeStateProof(t *testing.T) {
	key := []byte("account-2")
	value := []byte{0x7, 0x8, 0x9}
	tree, root := newCovTestJMTTree(t, key, value)

	proof, err := ProveStateInclusion(tree, root, key)
	if err != nil {
		t.Fatalf("ProveStateInclusion: %v", err)
	}

	encoded, err := EncodeStateProof(proof)
	if err != nil {
		t.Fatalf("EncodeStateProof: %v", err)
	}
	if len(encoded) < 32+4+len(key)+4+len(value) {
		t.Fatalf("encoded proof too short: %d bytes", len(encoded))
	}
	// First 32 bytes must be the state root.
	if types.Hash(encoded[:32]) != root {
		t.Fatalf("encoded state root mismatch")
	}
}

func TestEncodeStateProof_Nil(t *testing.T) {
	if _, err := EncodeStateProof(nil); err == nil {
		t.Fatal("expected error for nil proof")
	}
}
