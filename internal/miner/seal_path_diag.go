// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// S22 (docs/QS_BLOCK_TIME_BUDGET.md 6cs/6ct): "miner: seal path" -- ONE
// diagnostic info line per sealed block on the leader, carrying unix-ms
// stamps for every named step between the view's build trigger and the
// push, plus the two queue waits (resQWaitMs, taskQWaitMs) 6cs's own U1
// reading named as the next thing to measure directly. Gated on the SAME
// N42_CONTENTION_DIAG switch S14 introduced (internal/consensus/hotstuff's
// own contentionDiagEnabled): read independently here, one var per package,
// same pattern as every other shared-name switch in this campaign. Off by
// default: the log line is not emitted and the extra time.Now() calls this
// step adds are the only new cost (a handful of monotonic clock reads per
// sealed block either way -- no new lock, no per-transaction work).

package miner

import (
	"os"
	"time"
)

var contentionDiagEnabled = os.Getenv("N42_CONTENTION_DIAG") == "1"

// tMs returns t's unix-ms stamp, or 0 for a zero time.Time -- used
// throughout "miner: seal path" so an unreached step (e.g. specParkedAt on
// a fresh, never-speculative build) reads as a plain 0 rather than the
// large negative/garbage value UnixMilli() would give on a zero Time.
func tMs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// waitMs returns end-start as milliseconds, or 0 if either is zero (the
// step never happened, e.g. taskChSentAt on... never applicable today, but
// kept symmetric with tMs for every other derived wait below).
func waitMs(start, end time.Time) int64 {
	if start.IsZero() || end.IsZero() {
		return 0
	}
	d := end.Sub(start)
	if d < 0 {
		return 0
	}
	return d.Milliseconds()
}
