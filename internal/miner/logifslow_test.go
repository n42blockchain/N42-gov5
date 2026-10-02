// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers prefillTimes.logIfSlow's nil guard and its threshold (quiet under
// 50ms, logs over it -- the log line itself isn't asserted, just that
// calling it with a stale buildStart doesn't panic).

package miner

import (
	"testing"
	"time"
)

func TestLogIfSlowNilReceiverIsNoOp(t *testing.T) {
	var pf *prefillTimes
	pf.logIfSlow(1, 0, 0) // must not panic
}

func TestLogIfSlowQuietUnderThreshold(t *testing.T) {
	pf := &prefillTimes{buildStart: time.Now()}
	pf.logIfSlow(1, time.Millisecond, time.Millisecond) // elapsed ~0, under 50ms: no-op path
}

func TestLogIfSlowLogsOverThreshold(t *testing.T) {
	pf := &prefillTimes{buildStart: time.Now().Add(-100 * time.Millisecond)}
	pf.logIfSlow(1, 10*time.Millisecond, 5*time.Millisecond) // elapsed >50ms: exercises the log.Info path
}
