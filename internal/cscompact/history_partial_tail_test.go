// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package cscompact

import (
	"context"
	"testing"
)

// TestHistoryPartialTailRewritten is the weekly-rebuild shape: build to a
// mid-segment height, then build again to a later mid-segment height. Before
// the positional peel the second call was a no-op — resume computed
// existingSegs*HistSegmentSize, which already exceeded the new endBlock, so the
// tail segment kept its first-run coverage forever. That is exactly how
// accthist/storhist stayed frozen at their 2026-06-06 tail.
func TestHistoryPartialTailRewritten(t *testing.T) {
	dir := t.TempDir()

	// One key changes at every block we build over, so each segment is non-empty
	// and the per-key block list grows with coverage.
	key := make([]byte, 20)
	key[19] = 7
	changed := func(block uint64) ([][]byte, error) {
		return [][]byte{key}, nil
	}

	b := NewAccountHistoryBuilder(nil, dir)
	if err := b.BuildFromBlockKeys(context.Background(), 0, 1200, changed); err != nil {
		t.Fatalf("first build: %v", err)
	}
	if got, ok := lookupAt(t, dir, key, 3399); !ok || got != 1199 {
		t.Fatalf("after first build Lookup(3399) = %d,%v; want 1199,true", got, ok)
	}

	// Second run extends within the SAME (still partial) segment.
	if err := b.BuildFromBlockKeys(context.Background(), 0, 3400, changed); err != nil {
		t.Fatalf("second build: %v", err)
	}
	got, ok := lookupAt(t, dir, key, 3399)
	if !ok || got != 3399 {
		t.Fatalf("after second build Lookup(3399) = %d,%v; want 3399,true — the partial tail segment was not rewritten", got, ok)
	}
}

func lookupAt(t *testing.T, dir string, key []byte, at uint64) (uint64, bool) {
	t.Helper()
	r, err := NewHistoryReader(dir, "accthist")
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer r.Close()
	got, ok := r.Lookup(key, at)
	return got, ok
}
