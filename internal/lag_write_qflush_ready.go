// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import "os"

// lagWriteQFlushReady is N42_LAG_WRITE_QFLUSH_READY (default off, r89a;
// acts only with N42_LAG_WRITE_TXN_EARLY).
//
// The early transaction (lag_write_txn_early.go) writes the QMDB rows right
// after the block rows, before the receipts and the state. In E1N26 run 68
// that flush took 14.9 ms (mean) against 9-10 ms in the control leg, which
// wrote it after the state rows. Two things changed under it: it now waits
// inside the transaction for the hand-off's capture of the rows
// (N42_LAG_HANDOFF_LEAN starts it at the hand-off; the transaction opens
// sooner than before, so the capture is more often still running), and it
// runs while the state preparation is still working on other cores. The
// block write now stamps both (qCapWaitUs, qPrepBusy).
//
// With the switch on the early transaction writes the block rows and the
// receipts first, then the QMDB rows and the state in the order their
// inputs are ready: the QMDB rows first only when the preparation is still
// running and the capture is done (the flush fills a wait the transaction
// would otherwise idle in), else the state first (its preparation done, or
// the capture still running). The groups write disjoint tables, so the
// transaction holds the same rows; it is one transaction either way.
var lagWriteQFlushReady = parseOptIn(os.Getenv("N42_LAG_WRITE_QFLUSH_READY"))

// SetLagWriteQFlushReadyForTest switches N42_LAG_WRITE_QFLUSH_READY and
// returns the restore.
func SetLagWriteQFlushReadyForTest(on bool) func() {
	prev := lagWriteQFlushReady
	lagWriteQFlushReady = on
	return func() { lagWriteQFlushReady = prev }
}

// lagQFlushFirstForTest, when set, decides the order instead (tests).
var lagQFlushFirstForTest func(number uint64) bool

// lagQFlushFirst reports whether the QMDB rows go before the state rows:
// the preparation is still running and the capture is done. When both are
// still running it waits for the first of them.
func lagQFlushFirst(job *lagWriteJob) bool {
	if fn := lagQFlushFirstForTest; fn != nil {
		return fn(job.number)
	}
	if job.prep == nil || chanClosed(job.prep.done) {
		return false
	}
	if job.capture == nil {
		return true
	}
	select {
	case <-job.capture.Done():
		return !chanClosed(job.prep.done)
	case <-job.prep.done:
		return false
	}
}

// chanClosed reports whether ch is closed (never blocks).
func chanClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
