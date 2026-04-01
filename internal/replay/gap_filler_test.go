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

func TestDefaultConfigV2(t *testing.T) {
	cfg := DefaultConfigV2()
	if cfg.BatchSize != 100000 {
		t.Fatalf("BatchSize = %d, want 100000", cfg.BatchSize)
	}
	if !cfg.EnableJMT || !cfg.EnableLtHash || !cfg.DisableGC {
		t.Fatal("JMT/LtHash/DisableGC should be enabled by default")
	}
	if cfg.GapPeriod != 8 || cfg.GapTolerance != 15 {
		t.Fatalf("gap defaults wrong: period=%d tolerance=%d", cfg.GapPeriod, cfg.GapTolerance)
	}
}
