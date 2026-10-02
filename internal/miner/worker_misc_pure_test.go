// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Cheap, cheap wins: signalToErr's four branches and
// parallelFillEnabled's env-var switch.

package miner

import (
	"testing"
)

// TestSignalToErrUndefinedSignal covers the default branch that the
// existing TestSignalToErr (miner_test.go) doesn't exercise.
func TestSignalToErrUndefinedSignal(t *testing.T) {
	if err := signalToErr(999); err == nil {
		t.Fatalf("expected a non-nil error for an undefined signal")
	}
}

func TestParallelFillEnabled(t *testing.T) {
	t.Setenv("N42_MINER_PARALLEL_FILL", "")
	if parallelFillEnabled() {
		t.Fatalf("expected parallel fill off by default")
	}
	t.Setenv("N42_MINER_PARALLEL_FILL", "1")
	if !parallelFillEnabled() {
		t.Fatalf("expected parallel fill on when set to 1")
	}
}
