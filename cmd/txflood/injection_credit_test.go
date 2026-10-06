// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import "testing"

// S47 (docs/QS_BLOCK_TIME_BUDGET.md 6e7): -target-depth and -rate combine
// via injectionCredit -- the depth throttle's own shortfall capped so it
// never exceeds rate. These pin the three named scenarios the task asked
// for, plus the two single-flag boundaries injectionCredit's own callers
// rely on (a "keep the two single-flag modes byte-for-byte as today"
// check, expressed as a test rather than only as a code comment).
func TestInjectionCreditCombines(t *testing.T) {
	cases := []struct {
		name                          string
		targetDepth, depth, rate      int
		want                          int
	}{
		// Rate binds when depth is low (huge shortfall): the pool is nearly
		// empty (depth=1000 against a 300000 target, a 299000 shortfall),
		// but the generator must still not exceed its own requested ceiling.
		{"rate binds when depth is low", 300000, 1000, 16000, 16000},
		// Depth binds when the estimate is high: the shortfall (10000) is
		// already smaller than the rate ceiling (16000), so the depth
		// throttle's own number -- not the rate ceiling -- is what limits
		// this tick.
		{"depth binds when estimate is high (shortfall below rate)", 300000, 290000, 16000, 10000},
		// Depth binds harder still: the estimate reads AT OR OVER target
		// (a negative/zero shortfall) -- inject nothing this tick,
		// regardless of how large rate is. This is the actual overflow
		// guard S47 is for: 6e6's own inflated estimate crossing target
		// must stop injection, not just cap it at rate.
		{"depth binds when estimate is at or over target", 300000, 300000, 16000, 0},
		{"depth binds when estimate is over target (negative shortfall)", 300000, 310000, 16000, -10000},
		// Both zero: neither throttle constrains this tick -- unlimited.
		// The real depth-branch call site never actually invokes
		// injectionCredit with targetDepth<=0 (it is gated on
		// `if *targetDepth > 0` before ever calling this), but the
		// function's own contract is defined and tested regardless.
		{"both zero is unlimited", 0, 12345, 0, unlimitedCredit},
		// Single-flag: rate only (targetDepth off) -- reduces to the rate
		// ceiling alone, independent of depth's own reading.
		{"rate only, depth off", 0, 999999, 16000, 16000},
		// Single-flag: depth only (rate off) -- reduces to the shortfall
		// alone, matching today's pre-S47 depth-only behaviour exactly
		// (no rate cap ever applied).
		{"depth only, rate off", 300000, 1000, 0, 299000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := injectionCredit(c.targetDepth, c.depth, c.rate); got != c.want {
				t.Fatalf("injectionCredit(%d, %d, %d) = %d, want %d",
					c.targetDepth, c.depth, c.rate, got, c.want)
			}
		})
	}
}
