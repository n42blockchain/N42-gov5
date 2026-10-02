// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"strings"
	"testing"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func ethTMkFreezer(t *testing.T) *freezer.Freezer {
	t.Helper()
	fz, err := freezer.New(t.TempDir(), freezer.DefaultFreezeThreshold)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fz.Close() })
	return fz
}

func TestDropFrontEntries(t *testing.T) {
	mk := func() [][]byte { return [][]byte{{1}, {2}, {3}, {4}} }

	if got := dropFrontEntries(mk(), 0); len(got) != 4 {
		t.Fatalf("n=0: got %d entries, want 4 unchanged", len(got))
	}
	if got := dropFrontEntries(mk(), -1); len(got) != 4 {
		t.Fatalf("n<0: got %d entries, want 4 unchanged", len(got))
	}
	if got := dropFrontEntries(mk(), 4); len(got) != 0 {
		t.Fatalf("n==len: got %d entries, want 0", len(got))
	}
	if got := dropFrontEntries(mk(), 10); len(got) != 0 {
		t.Fatalf("n>len: got %d entries, want 0", len(got))
	}
	got := dropFrontEntries(mk(), 2)
	if len(got) != 2 || got[0][0] != 3 || got[1][0] != 4 {
		t.Fatalf("partial drop = %v, want [[3] [4]]", got)
	}
}

func TestOutputBatcherAddEntryAccumulatesAndTracksPending(t *testing.T) {
	fz := ethTMkFreezer(t)
	b, err := newOutputBatcher(fz)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if err := b.addEntry("acctcs", "c", []byte("a0")); err != nil {
		t.Fatal(err)
	}
	if err := b.addEntry("acctcs", "c", []byte("a1")); err != nil {
		t.Fatal(err)
	}
	if n := b.pendingCount(); n != 2 {
		t.Fatalf("pendingCount = %d, want 2", n)
	}
	if n := b.blocksSinceFlush(); n != 2 {
		t.Fatalf("blocksSinceFlush = %d, want 2", n)
	}
	// Below BatchSize: flushFullBatches is a no-op.
	n, err := b.flushFullBatches()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("flushFullBatches = %d, want 0 below BatchSize", n)
	}

	if err := b.sync(); err != nil {
		t.Fatal(err)
	}

	if err := b.flushAll(); err != nil {
		t.Fatal(err)
	}
	if n := b.blocksSinceFlush(); n != 0 {
		t.Fatalf("blocksSinceFlush after flushAll = %d, want 0", n)
	}
	if b.nextItem != 2 {
		t.Fatalf("nextItem = %d, want 2", b.nextItem)
	}
}

func TestOutputBatcherAddEntryMisalignedErrors(t *testing.T) {
	fz := ethTMkFreezer(t)
	b, err := newOutputBatcher(fz)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// Manually simulate a table that is ahead of nextItem+pending.
	if err := b.addEntry("acctcs", "c", []byte("a0")); err != nil {
		t.Fatal(err)
	}
	b.tables["acctcs"].existingItems = 100

	if err := b.addEntry("acctcs", "c", []byte("a1")); err == nil {
		t.Fatal("expected misalignment error")
	} else if !strings.Contains(err.Error(), "addEntry misaligned") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOutputBatcherFlushFullBatchesWritesCompleteBatches(t *testing.T) {
	fz := ethTMkFreezer(t)
	b, err := newOutputBatcher(fz)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	for i := 0; i < freezer.BatchSize+5; i++ {
		if err := b.addEntry("acctcs", "c", []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := b.flushFullBatches()
	if err != nil {
		t.Fatal(err)
	}
	if n != freezer.BatchSize {
		t.Fatalf("flushFullBatches = %d, want %d", n, freezer.BatchSize)
	}
	if got := b.blocksSinceFlush(); got != 5 {
		t.Fatalf("remaining pending = %d, want 5", got)
	}
	if err := b.flushAll(); err != nil {
		t.Fatal(err)
	}
	if got := b.blocksSinceFlush(); got != 0 {
		t.Fatalf("remaining pending after flushAll = %d, want 0", got)
	}
}

func TestOutputBatcherRemainderRoundTripsThroughPreload(t *testing.T) {
	fz := ethTMkFreezer(t)
	b, err := newOutputBatcher(fz)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if err := b.addEntry("acctcs", "c", []byte("aa")); err != nil {
		t.Fatal(err)
	}
	if err := b.addEntry("acctcs", "c", []byte("bb")); err != nil {
		t.Fatal(err)
	}
	// Every active table must accumulate the same per-block count.
	if err := b.addEntry("storcs", "c", []byte("cc")); err != nil {
		t.Fatal(err)
	}
	if err := b.addEntry("storcs", "c", []byte("dd")); err != nil {
		t.Fatal(err)
	}
	if err := b.flushAll(); err != nil {
		t.Fatal(err)
	}

	saved := b.remainder()
	if len(saved) != 0 {
		t.Fatalf("remainder after flushAll should be empty, got %v", saved)
	}

	// Re-add without flushing to exercise the non-empty remainder path.
	if err := b.addEntry("acctcs", "c", []byte("cc")); err != nil {
		t.Fatal(err)
	}
	saved = b.remainder()
	if len(saved["acctcs"]) == 0 {
		t.Fatal("expected a non-empty remainder blob for acctcs")
	}

	fz2 := ethTMkFreezer(t)
	b2, err := newOutputBatcher(fz2)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	if err := b2.addEntry("acctcs", "c", []byte("placeholder")); err != nil {
		t.Fatal(err)
	}
	b2.tables["acctcs"].entries = nil // simulate a fresh restart

	b2.preloadRemainder(saved)
	if len(b2.tables["acctcs"].entries) != 1 || string(b2.tables["acctcs"].entries[0]) != "cc" {
		t.Fatalf("preloadRemainder did not restore entries: %+v", b2.tables["acctcs"].entries)
	}

	// Unknown table name and a too-short/corrupt blob are both ignored.
	b2.preloadRemainder(map[string][]byte{"unknown": {1, 2, 3}, "acctcs": {0, 0}})
}

func TestOutputBatcherAlignOnResumeFreshTables(t *testing.T) {
	fz := ethTMkFreezer(t)
	b, err := newOutputBatcher(fz)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if err := b.alignOnResume([]string{"acctcs", "storcs"}, 0, false); err != nil {
		t.Fatal(err)
	}
	if b.nextItem != 0 {
		t.Fatalf("nextItem = %d, want 0", b.nextItem)
	}
	if len(b.order) != 2 {
		t.Fatalf("order = %v, want 2 tables", b.order)
	}
}

func TestOutputBatcherAlignOnResumeTruncatesAheadTable(t *testing.T) {
	fz := ethTMkFreezer(t)
	b, err := newOutputBatcher(fz)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < freezer.BatchSize; i++ {
		if err := b.addEntry("acctcs", "c", []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.flushAll(); err != nil {
		t.Fatal(err)
	}
	b.Close()

	// Re-open against the same freezer; the table is "ahead" of a smaller
	// MDBX startBlock, so alignOnResume must truncate it back.
	b2, err := newOutputBatcher(fz)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	if err := b2.alignOnResume([]string{"acctcs"}, 10, false); err != nil {
		t.Fatal(err)
	}
	// 10 isn't a batch boundary, so after the ahead-table truncate to 10 the
	// tail-alignment logic further recovers the partial batch down to the
	// batch boundary (0), restoring the 10 recovered entries in memory.
	tb := b2.tables["acctcs"]
	if tb.existingItems != 0 {
		t.Fatalf("existingItems = %d, want 0 (batch boundary)", tb.existingItems)
	}
	if len(tb.entries) != 10 {
		t.Fatalf("recovered entries = %d, want 10", len(tb.entries))
	}
}

func TestOutputBatcherAlignOnResumePadsBehindTable(t *testing.T) {
	fz := ethTMkFreezer(t)
	b, err := newOutputBatcher(fz)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// No prior data: items=0 < startBlock=5, gap <= BatchSize, no
	// remainder => pad with empty entries. 5 isn't a batch boundary, so
	// the tail-alignment logic then recovers the padded tail back down
	// to the batch boundary (0), with 5 recovered (empty) entries.
	if err := b.alignOnResume([]string{"acctcs"}, 5, false); err != nil {
		t.Fatal(err)
	}
	tb := b.tables["acctcs"]
	if got := tb.existingItems; got != 0 {
		t.Fatalf("existingItems after pad+recover = %d, want 0", got)
	}
	if got := len(tb.entries); got != 5 {
		t.Fatalf("recovered entries after pad = %d, want 5", got)
	}
}

func TestOutputBatcherAlignOnResumeGapCoveredByRemainder(t *testing.T) {
	fz := ethTMkFreezer(t)
	b, err := newOutputBatcher(fz)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// hasRemainder=true: the gap is left for preloadRemainder, not padded.
	if err := b.alignOnResume([]string{"acctcs"}, 5, true); err != nil {
		t.Fatal(err)
	}
	if got := b.tables["acctcs"].existingItems; got != 0 {
		t.Fatalf("existingItems = %d, want 0 (gap deferred to remainder)", got)
	}
}

func TestOutputBatcherAlignOnResumeUnrecoverableGapErrors(t *testing.T) {
	fz := ethTMkFreezer(t)
	b, err := newOutputBatcher(fz)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// gap > BatchSize with no remainder is a hard error.
	err = b.alignOnResume([]string{"acctcs"}, uint64(freezer.BatchSize)+1, false)
	if err == nil {
		t.Fatal("expected error for an unrecoverable gap")
	}
	if !strings.Contains(err.Error(), "changesets lost") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOutputBatcherAlignOnResumeRecoversPartialBatch(t *testing.T) {
	fz := ethTMkFreezer(t)
	b, err := newOutputBatcher(fz)
	if err != nil {
		t.Fatal(err)
	}
	// Write a partial batch (less than BatchSize) directly, then flushAll
	// to persist the tail as its own frame.
	for i := 0; i < 7; i++ {
		if err := b.addEntry("acctcs", "c", []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.flushAll(); err != nil {
		t.Fatal(err)
	}
	b.Close()

	b2, err := newOutputBatcher(fz)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	if err := b2.alignOnResume([]string{"acctcs"}, 7, false); err != nil {
		t.Fatal(err)
	}
	if got := len(b2.tables["acctcs"].entries); got != 7 {
		t.Fatalf("recovered entries = %d, want 7", got)
	}
	if got := b2.tables["acctcs"].existingItems; got != 0 {
		t.Fatalf("existingItems after recovery = %d, want 0 (batch boundary)", got)
	}
}

func TestOutputBatcherAlignOnResumeTruncatesPartialWhenRemainderAuthoritative(t *testing.T) {
	fz := ethTMkFreezer(t)
	b, err := newOutputBatcher(fz)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		if err := b.addEntry("acctcs", "c", []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.flushAll(); err != nil {
		t.Fatal(err)
	}
	b.Close()

	b2, err := newOutputBatcher(fz)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	if err := b2.alignOnResume([]string{"acctcs"}, 7, true); err != nil {
		t.Fatal(err)
	}
	if got := len(b2.tables["acctcs"].entries); got != 0 {
		t.Fatalf("entries = %d, want 0 (remainder is authoritative)", got)
	}
}

// TestOutputBatcherFlushOneBatchPartialOverlap exercises flushOneBatch's
// partial-overlap branch: a table whose existingItems falls strictly
// between the batch's start and end writes only the uncovered tail.
func TestOutputBatcherFlushOneBatchPartialOverlap(t *testing.T) {
	fz := ethTMkFreezer(t)
	b, err := newOutputBatcher(fz)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	const n = 10
	for i := 0; i < n; i++ {
		if err := b.addEntry("acctcs", "c", []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	// Pretend the first 4 of this batch are already on disk.
	b.tables["acctcs"].existingItems = 4

	if err := b.flushOneBatch(n); err != nil {
		t.Fatal(err)
	}
	if b.nextItem != n {
		t.Fatalf("nextItem = %d, want %d", b.nextItem, n)
	}
	// Only the uncovered tail (n - existingItems = 6) was written and
	// dropped from the in-memory entries.
	if got := len(b.tables["acctcs"].entries); got != 4 {
		t.Fatalf("entries after partial-overlap flush = %d, want 4", got)
	}
	tbl := b.tables["acctcs"].tbl
	if got := tbl.Items(); got != n-4 {
		t.Fatalf("table Items() = %d, want %d", got, n-4)
	}
}

func TestPadTableTo(t *testing.T) {
	fz := ethTMkFreezer(t)
	tbl, err := fz.EnsureTableCompressed("padtest", "c")
	if err != nil {
		t.Fatal(err)
	}
	b, err := newOutputBatcher(fz)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if err := padTableTo(tbl, 10, b.enc); err != nil {
		t.Fatal(err)
	}
	if got := tbl.Items(); got != 10 {
		t.Fatalf("Items() = %d, want 10", got)
	}
	// Already at/above target: no-op.
	if err := padTableTo(tbl, 5, b.enc); err != nil {
		t.Fatal(err)
	}
	if got := tbl.Items(); got != 10 {
		t.Fatalf("Items() after no-op pad = %d, want 10", got)
	}
}

func TestOutputBatcherVerifyExistingMatchMismatchAndEmptyEntries(t *testing.T) {
	fz := ethTMkFreezer(t)
	b, err := newOutputBatcher(fz)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// Build a table with one existing on-disk batch of known content.
	for i := 0; i < freezer.BatchSize; i++ {
		if err := b.addEntry("acctcs", "c", []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.flushAll(); err != nil {
		t.Fatal(err)
	}
	tb := b.tables["acctcs"]
	tb.existingItems = uint64(freezer.BatchSize)

	// verified already true: early return, no-op.
	tb.verified = true
	b.verifyExisting(tb, 0, freezer.BatchSize)
	tb.verified = false

	// count==0: early return.
	b.verifyExisting(tb, 0, 0)
	if tb.verified {
		t.Fatal("count=0 must not mark verified")
	}

	// No local entries to compare against: marks verified on first read.
	b.verifyExisting(tb, 0, freezer.BatchSize)
	if !tb.verified {
		t.Fatal("expected verified=true when there are no local entries to compare")
	}

	// Matching entries: a full-length slice so sampleIdx (count/2) lands on
	// a real entry, matching the on-disk byte(i) content exactly.
	matching := make([][]byte, freezer.BatchSize)
	for i := range matching {
		matching[i] = []byte{byte(i)}
	}
	tb.verified = false
	tb.entries = matching
	b.verifyExisting(tb, 0, freezer.BatchSize)
	if !tb.verified {
		t.Fatal("expected verified=true for matching sampled entry")
	}

	// Mismatched entries accumulate warnCount and eventually self-verify.
	mismatched := make([][]byte, freezer.BatchSize)
	for i := range mismatched {
		mismatched[i] = []byte{0xFF}
	}
	tb.verified = false
	tb.warnCount = 0
	tb.entries = mismatched
	for i := 0; i < 3; i++ {
		b.verifyExisting(tb, 0, freezer.BatchSize)
	}
	if !tb.verified {
		t.Fatal("expected verified=true after repeated mismatches (warnCount>=3)")
	}
	if tb.warnCount < 3 {
		t.Fatalf("warnCount = %d, want >= 3", tb.warnCount)
	}

	// sampleIdx beyond entries length marks verified without comparing.
	tb.verified = false
	tb.entries = [][]byte{}
	b.verifyExisting(tb, 0, freezer.BatchSize)
	if !tb.verified {
		t.Fatal("expected verified=true when sampleIdx is out of range")
	}
}
