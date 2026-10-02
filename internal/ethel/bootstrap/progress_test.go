// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package bootstrap

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

	cb := p.callback("asset-A")

	cb(fetch.Progress{Bytes: 10, Total: 0, Source: "https"})
	if _, ok := p.lastLog["asset-A"]; !ok {
		t.Fatal("expected lastLog to be set after first callback")
	}

	before := p.lastLog["asset-A"]
	cb(fetch.Progress{Bytes: 20, Total: 100, Source: "https"})
	if p.lastLog["asset-A"] != before {
		t.Error("throttled call should not update lastLog")
	}

	cb(fetch.Progress{Bytes: 100, Total: 100, Source: "https"})
	if p.lastLog["asset-A"] == before {
		t.Error("done call should flush despite throttle window")
	}

	cb2 := p.callback("asset-B")
	cb2(fetch.Progress{Bytes: 5, Total: 50, Source: "torrent"})
	if _, ok := p.lastLog["asset-B"]; !ok {
		t.Error("expected lastLog entry for asset-B")
	}
}
