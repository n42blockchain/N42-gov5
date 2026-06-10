// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import "testing"

func TestCalcGapBlocks(t *testing.T) {
	tests := []struct {
		name      string
		prevTime  uint64
		currTime  uint64
		period    uint64
		tolerance uint64
		wantCount int
	}{
		{"no gap", 1000, 1008, 8, 15, 0},
		{"at tolerance", 1000, 1015, 8, 15, 0},
		{"one gap block", 1000, 1016, 8, 15, 1},
		{"three gap blocks", 1000, 1032, 8, 15, 3},
		{"large gap", 1000, 1100, 8, 15, 12},
		{"zero prev", 0, 1000, 8, 15, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			times := CalcGapBlockTimes(tt.prevTime, tt.currTime, tt.period, tt.tolerance)
			if len(times) != tt.wantCount {
				t.Fatalf("got %d blocks, want %d", len(times), tt.wantCount)
			}
			for i, ts := range times {
				if ts <= tt.prevTime || ts >= tt.currTime {
					t.Errorf("gap block %d: time %d not in (%d, %d)", i, ts, tt.prevTime, tt.currTime)
				}
				if i > 0 && ts != times[i-1]+tt.period {
					t.Errorf("gap block %d: time %d, expected %d", i, ts, times[i-1]+tt.period)
				}
			}
		})
	}
}

// TestGapFillCapBoundsHugeGap: a multi-day startup/outage gap must be CAPPED so a
// single batch cannot synthesize hundreds of thousands of empty blocks and OOM.
func TestGapFillCapBoundsHugeGap(t *testing.T) {
	// 7.9-day gap (the real genesis->block1 gap), period 8s -> ~85k blocks uncapped.
	prev, curr := uint64(1678174066), uint64(1678855970)
	uncapped := CalcGapBlockTimesCapped(prev, curr, 8, 15, 0)
	if len(uncapped) < 80000 {
		t.Fatalf("sanity: expected ~85k uncapped, got %d", len(uncapped))
	}
	capped := CalcGapBlockTimesCapped(prev, curr, 8, 15, 10000)
	if len(capped) != 10000 {
		t.Fatalf("cap not enforced: got %d, want 10000", len(capped))
	}
	// capped result is a prefix of the uncapped timeline (no jumbling).
	for i := 0; i < 10000; i++ {
		if capped[i] != uncapped[i] {
			t.Fatalf("capped[%d]=%d != uncapped[%d]=%d", i, capped[i], i, uncapped[i])
		}
	}
}

// TestGapFillZeroCapUnlimited: cap=0 preserves the legacy unlimited behavior, and
// the 4-arg CalcGapBlockTimes matches the cap=0 form.
func TestGapFillZeroCapUnlimited(t *testing.T) {
	prev, curr := uint64(1000), uint64(100000)
	a := CalcGapBlockTimes(prev, curr, 8, 15)
	b := CalcGapBlockTimesCapped(prev, curr, 8, 15, 0)
	if len(a) != len(b) || len(a) == 0 {
		t.Fatalf("4-arg vs cap=0 mismatch: %d vs %d", len(a), len(b))
	}
}

func TestDefaultConfigV2(t *testing.T) {
	cfg := DefaultConfigV2()
	if cfg.BatchSize != 10000 {
		t.Fatalf("BatchSize = %d, want 10000", cfg.BatchSize)
	}
	if !cfg.EnableJMT || !cfg.EnableLtHash || !cfg.DisableGC {
		t.Fatal("JMT/LtHash/DisableGC should be enabled by default")
	}
	if cfg.GapPeriod != 8 || cfg.GapTolerance != 15 {
		t.Fatalf("gap defaults wrong: period=%d tolerance=%d", cfg.GapPeriod, cfg.GapTolerance)
	}
}
