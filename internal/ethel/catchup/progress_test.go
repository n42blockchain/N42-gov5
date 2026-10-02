// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package catchup

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/internal/ethel/fetch"
)

// TestProgressLoggerCallback drives every branch of callback: unknown total
// (bytes-only line), a throttled call that is skipped, and a "done" call that
// always flushes even inside the throttle window.
func TestProgressLoggerCallback(t *testing.T) {
	p := newProgressLogger()
	p.interval = time.Hour // make throttling deterministic

	cb := p.callback("seg-A")

	// First call: Total == 0 -> bytes-only branch. lastLog not set yet, so
	// this always logs regardless of throttle.
	cb(fetch.Progress{Bytes: 10, Total: 0, Source: "https"})
	if _, ok := p.lastLog["seg-A"]; !ok {
		t.Fatal("expected lastLog to be set after first callback")
	}

	// Second call, within the throttle interval, not done -> should be
	// skipped (no panic, no observable effect to assert other than it not
	// crashing and lastLog staying close to its prior value is not asserted,
	// since this exercises the "return" branch for coverage).
	before := p.lastLog["seg-A"]
	cb(fetch.Progress{Bytes: 20, Total: 100, Source: "https"})
	if p.lastLog["seg-A"] != before {
		t.Error("throttled call should not update lastLog")
	}

	// Done call (Bytes >= Total) inside the throttle window must still log
	// (bypasses the throttle) and update lastLog.
	cb(fetch.Progress{Bytes: 100, Total: 100, Source: "https"})
	if p.lastLog["seg-A"] == before {
		t.Error("done call should flush despite throttle window")
	}

	// A different segment name gets its own throttle bucket.
	cb2 := p.callback("seg-B")
	cb2(fetch.Progress{Bytes: 5, Total: 50, Source: "torrent"})
	if _, ok := p.lastLog["seg-B"]; !ok {
		t.Error("expected lastLog entry for seg-B")
	}
}
