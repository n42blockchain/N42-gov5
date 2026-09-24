// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import "testing"

// S39 (docs/QS_BLOCK_TIME_BUDGET.md 6dr/6ds): pins the setter/getter
// round-trip and the "never called" zero-value contract ImportStamps'
// own doc comment promises -- callers gate every setter behind their own
// N42_CONTENTION_DIAG, so a block that never passes through a given
// hand-off (or ran with the diag off) must read back exactly 0 for it,
// not some other sentinel.
func TestImportStampsRoundTrip(t *testing.T) {
	b := &Block{}

	if rxEnd, decStart, decEnd, chkStart, chkEnd, q, insDispatch, insStart := b.ImportStamps(); rxEnd != 0 || decStart != 0 || decEnd != 0 || chkStart != 0 || chkEnd != 0 || q != 0 || insDispatch != 0 || insStart != 0 {
		t.Fatalf("fresh Block: want all-zero stamps, got %d %d %d %d %d %d %d %d",
			rxEnd, decStart, decEnd, chkStart, chkEnd, q, insDispatch, insStart)
	}

	b.SetRxEndTMs(100)
	b.SetDecStamps(101, 105)
	b.SetCheckStamps(106, 110)
	b.SetQueueTMs(111)
	b.SetInsDispatchTMs(112)
	b.SetInsertStartTMs(115)

	rxEnd, decStart, decEnd, chkStart, chkEnd, q, insDispatch, insStart := b.ImportStamps()
	want := [8]int64{100, 101, 105, 106, 110, 111, 112, 115}
	got := [8]int64{rxEnd, decStart, decEnd, chkStart, chkEnd, q, insDispatch, insStart}
	if got != want {
		t.Fatalf("ImportStamps() = %v, want %v", got, want)
	}
}

// SetCheckStamps must overwrite, not accumulate: a block whose parent was
// not yet applied re-enters deferredCheck on retry, and only the LATEST
// attempt's stamps should survive (the deferred check that actually ran).
func TestSetCheckStampsOverwritesOnRetry(t *testing.T) {
	b := &Block{}
	b.SetCheckStamps(10, 20) // first attempt: parent not applied yet
	b.SetCheckStamps(50, 55) // retry: this is the one that ran to completion

	_, _, _, chkStart, chkEnd, _, _, _ := b.ImportStamps()
	if chkStart != 50 || chkEnd != 55 {
		t.Fatalf("SetCheckStamps: want the latest attempt (50, 55), got (%d, %d)", chkStart, chkEnd)
	}
}
