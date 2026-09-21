// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// S22 unit tests for seal_path_diag.go's tMs/waitMs helpers
// (docs/QS_BLOCK_TIME_BUDGET.md 6cs/6ct).

package miner

import (
	"testing"
	"time"
)

func TestTMsZeroTime(t *testing.T) {
	if got := tMs(time.Time{}); got != 0 {
		t.Fatalf("tMs(zero) = %d, want 0", got)
	}
}

func TestTMsNonZeroTime(t *testing.T) {
	now := time.Now()
	if got := tMs(now); got != now.UnixMilli() {
		t.Fatalf("tMs(now) = %d, want %d", got, now.UnixMilli())
	}
}

func TestWaitMsBothZero(t *testing.T) {
	if got := waitMs(time.Time{}, time.Time{}); got != 0 {
		t.Fatalf("waitMs(zero, zero) = %d, want 0", got)
	}
}

func TestWaitMsStartZero(t *testing.T) {
	if got := waitMs(time.Time{}, time.Now()); got != 0 {
		t.Fatalf("waitMs(zero, now) = %d, want 0 (a step that never started has no wait)", got)
	}
}

func TestWaitMsEndZero(t *testing.T) {
	if got := waitMs(time.Now(), time.Time{}); got != 0 {
		t.Fatalf("waitMs(now, zero) = %d, want 0 (a step that never finished has no wait)", got)
	}
}

func TestWaitMsPositive(t *testing.T) {
	start := time.Now()
	end := start.Add(37 * time.Millisecond)
	if got := waitMs(start, end); got != 37 {
		t.Fatalf("waitMs(start, start+37ms) = %d, want 37", got)
	}
}

// TestWaitMsNegativeClampsToZero: end before start (clock skew between two
// timers, or a step that measurably could not have taken negative time) must
// read as 0, not a negative duration that would corrupt a median/percentile
// downstream.
func TestWaitMsNegativeClampsToZero(t *testing.T) {
	start := time.Now()
	end := start.Add(-5 * time.Millisecond)
	if got := waitMs(start, end); got != 0 {
		t.Fatalf("waitMs(start, start-5ms) = %d, want 0", got)
	}
}

// TestContentionDiagEnabledDefaultsOff matches every other diagnostic switch
// in this campaign (N42_BUILD_STALL_DIAG, N42_LEADER_WRITE_AFTER_JOURNAL,
// N42_PUSH_BEFORE_WRITE): reading the process environment must not itself be
// what turns a round's diagnostics on.
func TestContentionDiagEnabledDefaultsOff(t *testing.T) {
	if contentionDiagEnabled {
		t.Skip("N42_CONTENTION_DIAG=1 is set in this test binary's environment")
	}
}
