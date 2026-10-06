// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// S15b: N42_BLOCK_GOSSIP_FALLBACK's parser and decision helper.
// docs/QS_BLOCK_TIME_BUDGET.md 6cg.

package internal

import "testing"

func TestParseBlockGossipFallback(t *testing.T) {
	for _, tc := range []struct {
		v    string
		want bool
	}{
		{"", true},        // unset: today's behaviour
		{"1", true},       // explicit on
		{"true", true},    // anything but "0" keeps today's behaviour
		{"0", false},      // the only value that turns it off
		{"garbage", true}, // unrecognized: fail open to today's behaviour
	} {
		if got := parseBlockGossipFallback(tc.v); got != tc.want {
			t.Errorf("parseBlockGossipFallback(%q) = %v, want %v", tc.v, got, tc.want)
		}
	}
}

// TestShouldGossipBlock covers the three cases the task named directly:
// zero peers always gossips (there is no other delivery path), a positive
// peer count with the switch off skips gossip, and the default (switch on)
// always gossips regardless of peer count.
func TestShouldGossipBlock(t *testing.T) {
	for _, tc := range []struct {
		name            string
		fallbackEnabled bool
		peerCount       int
		want            bool
	}{
		{"zero peers, switch off: still gossip", false, 0, true},
		{"zero peers, switch on: still gossip", true, 0, true},
		{"peers dispatched, switch off: no gossip", false, 3, false},
		{"peers dispatched, switch on (default): gossip", true, 3, true},
		{"one peer, switch off: no gossip", false, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldGossipBlock(tc.fallbackEnabled, tc.peerCount); got != tc.want {
				t.Errorf("shouldGossipBlock(%v, %d) = %v, want %v", tc.fallbackEnabled, tc.peerCount, got, tc.want)
			}
		})
	}
}
