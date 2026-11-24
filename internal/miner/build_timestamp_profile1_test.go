// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/params"
)

// B4: profile 1 stamps max(parent.time, floor(now)) with an equal timestamp
// allowed -- the legacy parent.time+1 bump is never taken. Legacy chains
// keep the strictly increasing bump.
func TestBuildTimestampProfile1(t *testing.T) {
	p1 := profile1Config()
	legacy := &params.ChainConfig{ChainID: big.NewInt(4242)}
	cases := []struct {
		parent, now, p1Want, legacyWant uint64
	}{
		{100, 100, 100, 101}, // equal: allowed on profile 1
		{100, 105, 105, 105}, // clock ahead
		{100, 99, 100, 101},  // clock behind the parent
		{0, 0, 0, 1},
	}
	for _, c := range cases {
		if got := buildTimestamp(p1, c.parent, c.now); got != c.p1Want {
			t.Errorf("profile 1 parent %d now %d: %d, want %d", c.parent, c.now, got, c.p1Want)
		}
		if got := buildTimestamp(legacy, c.parent, c.now); got != c.legacyWant {
			t.Errorf("legacy parent %d now %d: %d, want %d", c.parent, c.now, got, c.legacyWant)
		}
	}
}
