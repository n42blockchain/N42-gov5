// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers newAsyncBlockWriter itself (the one constructor async_write_test.go
// deliberately avoids, per its own top-of-file comment) with no jobs ever
// enqueued, so the goroutine it starts never calls w.writeAndFinish (which
// needs a real worker/blockchain) and simply exits cleanly on Drain.

package miner

import (
	"testing"
	"time"
)

func TestNewAsyncBlockWriterStartsAndDrainsCleanly(t *testing.T) {
	w := &worker{}
	aw := newAsyncBlockWriter(w)
	if aw.w != w {
		t.Fatalf("expected the writer to retain its owning worker")
	}
	if aw.jobCh == nil || cap(aw.jobCh) != 1 {
		t.Fatalf("expected a capacity-1 job channel")
	}
	if aw.process == nil {
		t.Fatalf("expected process to default to w.writeAndFinish")
	}
	if _, _, ok := aw.ExpectedParent(); ok {
		t.Fatalf("expected no pending jobs on a freshly created writer")
	}
	if !aw.Drain(2 * time.Second) {
		t.Fatalf("expected Drain to complete immediately with no jobs ever enqueued")
	}
}
