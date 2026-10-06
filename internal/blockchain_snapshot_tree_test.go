// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers SetSnapshotTree/SnapshotTree's round trip.

package internal

import "testing"

func TestSetAndGetSnapshotTree(t *testing.T) {
	bc := &BlockChain{}
	if bc.SnapshotTree() != nil {
		t.Fatalf("SnapshotTree() before Set = non-nil")
	}
	// A nil tree round-trips cleanly (the common "not configured" case).
	bc.SetSnapshotTree(nil)
	if bc.SnapshotTree() != nil {
		t.Fatalf("SnapshotTree() after SetSnapshotTree(nil) = non-nil")
	}
}
