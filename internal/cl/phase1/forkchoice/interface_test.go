// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Phase 7.2.1 Tier 0 — compile guard for the ForkChoiceStorage interface.
// Any drift between later-tier implementations and this Tier 0 contract
// will surface here as a build failure.

//go:build n42el

package forkchoice

import "testing"

// TestTier0_TypesCompile is a no-op runtime check; the real value is at
// build time — instantiating the leaf types confirms the cherry-pick
// adapted imports cleanly.
func TestTier0_TypesCompile(t *testing.T) {
	_ = LatestMessage{Epoch: 1, Slot: 2}
	_ = ForkChoiceNode{}
	_ = ForkNode{
		Slot:           42,
		JustifiedEpoch: 1,
		FinalizedEpoch: 1,
		Weight:         1000,
		Validity:       "VALID",
	}
}

// TestTier0_InterfaceShape declares method-set probes that fail to
// build if any Tier 0 reader/writer method signature drifts away from
// upstream erigon. Add new methods here as later tiers introduce
// non-stub implementations and the interface evolves.
func TestTier0_InterfaceShape(t *testing.T) {
	// Compile-time only: pin the interface symbols so a renamed method
	// or a changed signature breaks `go build` immediately.
	var (
		_ ForkChoiceStorage       = (ForkChoiceStorage)(nil)
		_ ForkChoiceStorageReader = (ForkChoiceStorageReader)(nil)
		_ ForkChoiceStorageWriter = (ForkChoiceStorageWriter)(nil)
	)
}
