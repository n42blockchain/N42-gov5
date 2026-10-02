// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package freezer

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestFreezerTableSetStartItem(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "start", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	// Empty table: can set start freely.
	if err := tbl.SetStartItem(1000); err != nil {
		t.Fatalf("SetStartItem: %v", err)
	}
	if got := tbl.StartItem(); got != 1000 {
		t.Fatalf("StartItem: got %d want 1000", got)
	}
	// Idempotent: same start again is a no-op success.
	if err := tbl.SetStartItem(1000); err != nil {
		t.Fatalf("SetStartItem idempotent: %v", err)
	}

	// Append an item, then non-empty table rejects a different start.
	if err := tbl.Append(1000, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tbl.SetStartItem(2000); err == nil {
		t.Fatalf("expected error setting start on non-empty table")
	}

	// Read-only table rejects SetStartItem.
	roTbl, err := NewFreezerTableReadOnly(dir, "start", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer roTbl.Close()
	if err := roTbl.SetStartItem(5000); err == nil {
		t.Fatalf("expected error on read-only table")
	}
}

func TestFreezerTableSetStartItemLegacyHeaderless(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "legacy", "c")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a legacy headerless table by zeroing idxHeaderSize.
	tbl.idxHeaderSize = 0
	if err := tbl.SetStartItem(10); err == nil {
		t.Fatalf("expected error on legacy headerless table")
	}
	tbl.Close()
}

func TestRetrimIndexToFile(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTableCompressed(dir, "byfile", "c")
	if err != nil {
		t.Fatal(err)
	}
	tbl.ForceBatchSize(4)
	// Force multiple small data files by writing many batches with a tiny
	// maxFileSize is not adjustable here, so instead just build several
	// batches via AppendBatchBlob directly on distinct "files" is complex;
	// instead exercise the not-found and basic-found paths using the
	// single-file table (fileNum always 0), which still exercises the
	// sort.Search and RetrimIndexToItem delegation logic.
	for b := 0; b < 4; b++ {
		var blob []byte
		for i := 0; i < 4; i++ {
			payload := []byte(fmt.Sprintf("p-%d", b*4+i))
			var lenPrefix [4]byte
			lenPrefix[0] = byte(len(payload))
			blob = append(blob, lenPrefix[:]...)
			blob = append(blob, payload...)
		}
		if err := tbl.AppendBatchBlob(uint64(b*4), 4, blob); err != nil {
			t.Fatal(err)
		}
	}
	if err := tbl.Close(); err != nil {
		t.Fatal(err)
	}

	// keepFromFile=0 keeps everything (all entries have fileNum 0).
	newStart, dropped, err := RetrimIndexToFile(dir, "byfile", "c", 0)
	if err != nil {
		t.Fatalf("RetrimIndexToFile: %v", err)
	}
	if newStart != 0 || dropped != 0 {
		t.Fatalf("got start=%d dropped=%d, want 0/0", newStart, dropped)
	}

	// keepFromFile=1 with everything in file 0 -> no entries qualify -> error.
	if _, _, err := RetrimIndexToFile(dir, "byfile", "c", 1); err == nil {
		t.Fatalf("expected error: no entries in files >= 1")
	}
}

func TestRetrimIndexToFileEmptyTable(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "emptyidx", "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.Close(); err != nil {
		t.Fatal(err)
	}
	newStart, dropped, err := RetrimIndexToFile(dir, "emptyidx", "c", 0)
	if err != nil {
		t.Fatalf("RetrimIndexToFile on empty table: %v", err)
	}
	if newStart != 0 || dropped != 0 {
		t.Fatalf("got start=%d dropped=%d, want 0/0", newStart, dropped)
	}
}

func TestFreezerTableWriteReadMeta(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "meta", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	// No .meta file yet -> ReadMeta returns (0, nil).
	got, err := tbl.ReadMeta()
	if err != nil || got != 0 {
		t.Fatalf("ReadMeta before write: got (%d,%v) want (0,nil)", got, err)
	}

	for i := uint64(0); i < 5; i++ {
		if err := tbl.Append(i, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tbl.WriteMeta(); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	got, err = tbl.ReadMeta()
	if err != nil || got != 5 {
		t.Fatalf("ReadMeta after write: got (%d,%v) want (5,nil)", got, err)
	}
}

func TestFreezerTableSetCompressed(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "setcomp", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	if tbl.compressed {
		t.Fatalf("expected table to start uncompressed")
	}
	tbl.SetCompressed(true)
	if !tbl.compressed {
		t.Fatalf("expected compressed=true after SetCompressed(true)")
	}
	tbl.SetCompressed(false)
	if tbl.compressed {
		t.Fatalf("expected compressed=false after SetCompressed(false)")
	}
}

func TestFreezerSync(t *testing.T) {
	dir := t.TempDir()
	f, err := New(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	data := covMakeFreezeData(3)
	if err := f.Freeze(0, data); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
}

func TestFreezerStartFreezeDoFreeze(t *testing.T) {
	dir := t.TempDir()
	// Low threshold so doFreeze's gate passes quickly.
	f, err := New(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var head atomic.Uint64
	head.Store(100)

	var cleanupCalls atomic.Int32
	freezeFn := func(start, count uint64) (*FreezeData, error) {
		return covMakeFreezeData(int(count)), nil
	}
	cleanupFn := func(start, count uint64) error {
		cleanupCalls.Add(1)
		return nil
	}

	// Directly exercise doFreeze (unexported, same package) rather than
	// waiting on the real timer-driven freezeLoop, so the test is fast and
	// deterministic.
	f.doFreeze(func() uint64 { return head.Load() }, freezeFn, cleanupFn)

	if f.Frozen() == 0 {
		t.Fatalf("expected doFreeze to freeze some blocks")
	}
	if cleanupCalls.Load() != 1 {
		t.Fatalf("expected cleanupFn called once, got %d", cleanupCalls.Load())
	}

	// head below threshold: no-op.
	before := f.Frozen()
	head.Store(0)
	f.doFreeze(func() uint64 { return head.Load() }, freezeFn, cleanupFn)
	if f.Frozen() != before {
		t.Fatalf("expected no additional freeze when head below threshold")
	}

	// freezeFn error path: should not panic, no change.
	head.Store(1000)
	errFreezeFn := func(start, count uint64) (*FreezeData, error) {
		return nil, errors.New("boom")
	}
	f.doFreeze(func() uint64 { return head.Load() }, errFreezeFn, cleanupFn)
	if f.Frozen() != before {
		t.Fatalf("expected no change on freezeFn error")
	}
}

func TestFreezerStartFreezeLoopIntegration(t *testing.T) {
	dir := t.TempDir()
	f, err := New(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	f.StartFreeze(ctx, func() uint64 { return 0 }, func(start, count uint64) (*FreezeData, error) {
		return covMakeFreezeData(int(count)), nil
	}, nil)

	// head always 0 <= threshold, so doFreeze never actually freezes; this
	// just exercises StartFreeze/freezeLoop goroutine lifecycle (start+stop)
	// without waiting out the real 30s interval.
	cancel()
	f.Close() // Close also cancels+waits; safe to call after our own cancel.
}

func TestFreezerCloseWithoutStartFreeze(t *testing.T) {
	dir := t.TempDir()
	f, err := New(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Close without ever calling StartFreeze: cancel is nil, must not panic.
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestFreezerTruncateHeadNoop(t *testing.T) {
	dir := t.TempDir()
	f, err := New(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	data := covMakeFreezeData(5)
	if err := f.Freeze(0, data); err != nil {
		t.Fatal(err)
	}
	// from >= frozen: no-op, returns nil.
	if err := f.TruncateHead(10); err != nil {
		t.Fatalf("TruncateHead no-op: %v", err)
	}
	if f.Frozen() != 5 {
		t.Fatalf("Frozen changed on no-op truncate: %d", f.Frozen())
	}
	if err := f.TruncateHead(2); err != nil {
		t.Fatalf("TruncateHead: %v", err)
	}
	if f.Frozen() != 2 {
		t.Fatalf("Frozen after truncate: got %d want 2", f.Frozen())
	}
}

func TestNewFreezerTableCompressedReadOnly(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTableCompressed(dir, "rocomp", "c")
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(0); i < 3; i++ {
		if err := tbl.Append(i, []byte(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := tbl.Close(); err != nil {
		t.Fatal(err)
	}

	ro, err := NewFreezerTableCompressedReadOnly(dir, "rocomp", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if ro.Items() != 3 {
		t.Fatalf("Items: got %d want 3", ro.Items())
	}
	if err := ro.Append(3, []byte("x")); err == nil {
		t.Fatalf("expected error appending to read-only compressed table")
	}
}

func TestFreezerFreezeInterval(t *testing.T) {
	// Sanity check the exported-ish constants used by freezeLoop stay sane,
	// without actually sleeping freezeInterval (30s) in a short test.
	if freezeInterval <= 0 {
		t.Fatalf("freezeInterval must be positive")
	}
	if freezeBatchSize == 0 {
		t.Fatalf("freezeBatchSize must be positive")
	}
	_ = time.Second
}
