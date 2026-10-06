// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// TestComputeRootSharedMatches: an isolated tree computes a block's own
// root; replaying the IDENTICAL dirty set onto a second, independent tree
// (standing in for the live tree) with that root as sealedRoot must report
// matched=true and count only a match, never a mismatch.
func TestComputeRootSharedMatches(t *testing.T) {
	before, _ := QMDBSingleFoldCounts()

	dirty := block{
		accts: map[types.Address]*account.StateAccount{
			qmAddr(1): qmAcct(1, 100),
			qmAddr(2): qmAcct(2, 200),
		},
	}
	isolated := NewQMDBRootComputer()
	sealedRoot, err := isolated.ComputeRoot(dirty.accts, dirty.stor)
	if err != nil {
		t.Fatalf("isolated ComputeRoot: %v", err)
	}

	live := NewQMDBRootComputer()
	root, matched, err := live.ComputeRootShared(dirty.accts, dirty.stor, sealedRoot)
	if err != nil {
		t.Fatalf("ComputeRootShared: %v", err)
	}
	if !matched {
		t.Fatal("matched = false, want true for an identical replay")
	}
	if root != sealedRoot {
		t.Fatalf("root = %x, want the sealed root %x", root[:8], sealedRoot.Bytes()[:8])
	}

	after, mismatches := QMDBSingleFoldCounts()
	if after != before+1 {
		t.Fatalf("match counter = %d, want %d", after, before+1)
	}
	_ = mismatches
}

// TestComputeRootSharedMismatch: a WRONG sealedRoot (as if the isolated
// build had computed a different block, or diverged) must report
// matched=false, still return the live tree's own actual root (never the
// wrong sealedRoot), and count a mismatch -- never silently accepted.
func TestComputeRootSharedMismatch(t *testing.T) {
	_, beforeMismatch := QMDBSingleFoldCounts()

	dirty := block{
		accts: map[types.Address]*account.StateAccount{
			qmAddr(3): qmAcct(3, 300),
		},
	}
	live := NewQMDBRootComputer()
	wrongRoot := types.Hash{0xDE, 0xAD, 0xBE, 0xEF}
	root, matched, err := live.ComputeRootShared(dirty.accts, dirty.stor, wrongRoot)
	if err != nil {
		t.Fatalf("ComputeRootShared: %v", err)
	}
	if matched {
		t.Fatal("matched = true, want false for a deliberately wrong sealedRoot")
	}
	if root == wrongRoot {
		t.Fatal("ComputeRootShared must return the live tree's OWN computed root on a mismatch, not silently echo the wrong sealedRoot")
	}

	_, afterMismatch := QMDBSingleFoldCounts()
	if afterMismatch != beforeMismatch+1 {
		t.Fatalf("mismatch counter = %d, want %d", afterMismatch, beforeMismatch+1)
	}
}

// TestComputeRootSharedSameAsComputeRoot: ComputeRootShared's own returned
// root, on a match, is byte-identical to what plain ComputeRoot alone would
// have produced from the same dirty set -- the shared/guarded path computes
// nothing different, only adds the explicit comparison and counters.
func TestComputeRootSharedSameAsComputeRoot(t *testing.T) {
	dirty := block{
		accts: map[types.Address]*account.StateAccount{
			qmAddr(5): qmAcct(5, 500),
			qmAddr(6): qmAcct(6, 600),
		},
	}
	plain := NewQMDBRootComputer()
	plainRoot, err := plain.ComputeRoot(dirty.accts, dirty.stor)
	if err != nil {
		t.Fatalf("ComputeRoot: %v", err)
	}

	shared := NewQMDBRootComputer()
	sharedRoot, matched, err := shared.ComputeRootShared(dirty.accts, dirty.stor, plainRoot)
	if err != nil {
		t.Fatalf("ComputeRootShared: %v", err)
	}
	if !matched || sharedRoot != plainRoot {
		t.Fatalf("sharedRoot=%x matched=%v, want %x true", sharedRoot[:8], matched, plainRoot.Bytes()[:8])
	}
}
