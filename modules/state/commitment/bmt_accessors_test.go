// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers the small BMT accessor surface left untested by the canonical-root
// regression test: Tree() (used by callers that flush the tree to an external
// store) and RootScheme() (used by replay-v2 to pick the state-root engine).

package commitment

import (
	"testing"

	"github.com/n42blockchain/N42/lib/bmt"
	"github.com/n42blockchain/N42/modules/state"
)

func TestBMTCommitmentTree(t *testing.T) {
	tree := bmt.New(bmt.NewMemStore())
	c := NewBMTCommitment(tree)
	if c.Tree() != tree {
		t.Fatal("Tree() must return the exact tree instance passed to NewBMTCommitment")
	}
}

func TestBMTRootComputerRootScheme(t *testing.T) {
	rc := freshBMTComputer()
	if got := rc.RootScheme(); got != state.RootSchemeBMTBlake3 {
		t.Fatalf("RootScheme() = %v, want %v", got, state.RootSchemeBMTBlake3)
	}
}
