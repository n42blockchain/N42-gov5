// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import "testing"

// TestEarlyFlushGateCoversEveryBuffer pins the gate against the 2026-08-31
// out-of-memory: maybeEarlyFlush used to check only the leaf/chg buffers, which
// --records-only never fills (putLeaf returns early, chg aggregation is
// skipped). The node-record buffers it DOES fill were unchecked, so the gate
// was unreachable in Pipeline B and memory grew to the batch boundary — 75 GB
// private with 0.4 GB left on the machine.
//
// The test drives the predicate directly rather than a real build, so it stays
// a unit test: fill exactly one buffer past the threshold at a time and assert
// the gate fires for every single one.
func TestEarlyFlushGateCoversEveryBuffer(t *testing.T) {
	over := make([]kvPair, bufFlushThreshold+1)

	bufs := []struct {
		name string
		set  func(b *builder)
	}{
		{"chgAccBuf", func(b *builder) { b.chgAccBuf = over }},
		{"chgStoBuf", func(b *builder) { b.chgStoBuf = over }},
		{"leafABuf", func(b *builder) { b.leafABuf = over }},
		{"leafSBuf", func(b *builder) { b.leafSBuf = over }},
		// The three that --records-only actually fills.
		{"nodeAccBuf", func(b *builder) { b.nodeAccBuf = over }},
		{"nodeStoBuf", func(b *builder) { b.nodeStoBuf = over }},
		{"stoRootBuf", func(b *builder) { b.stoRootBuf = over }},
	}

	for _, tc := range bufs {
		b := &builder{}
		tc.set(b)
		if !earlyFlushNeeded(b) {
			t.Errorf("gate does NOT fire on %s past bufFlushThreshold — that buffer can grow unbounded", tc.name)
		}
	}

	// A builder with everything empty must not flush.
	if earlyFlushNeeded(&builder{}) {
		t.Error("gate fires on an empty builder")
	}
}
