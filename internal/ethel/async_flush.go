// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// async_flush.go — background PlainStateBuffer flusher.
//
// At every commit interval the executor used to do FlushToMDBX +
// tx.Commit synchronously, blocking the EVM for ~25 s at the 11M-block
// scale. asyncFlusher hides that latency by running the flush on a
// dedicated goroutine while the executor continues processing the next
// commit interval against a fresh write buffer + MDBX RoTx.
//
// Reader correctness during the in-flight window is provided by
// PlainStateBuffer.inFlight: BufferedPlainStateReader checks the active
// buffer → in-flight snapshot → LRU → MDBX. The snapshot stays pinned
// until the *next* commit interval, so the main thread's mid-window RoTx
// (with a stale snapshot vs. the bg commit) never returns wrong data.
//
// Crash consistency: the output freezer is still flushed synchronously
// at every commit interval, so on crash the output may be ahead of
// PlainState by up to one commit interval. Recovery replays the missing
// range from the V2 changesets via the rebuild-state forward path.

package ethel

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/state"
)

// asyncFlushResult carries the bg flush outcome back to the main thread.
type asyncFlushResult struct {
	err error
	dur time.Duration
}

// asyncFlusher hands off snapshots to a single background goroutine
// that opens its OWN MDBX RwTx, applies the snapshot, writes progress,
// and commits. The main thread keeps a separate RoTx for the duration
// of the next exec interval — MDBX permits concurrent {1 RwTx, N RoTx}
// so the two run in parallel without serializing.
//
// At most one flush is in flight; back-pressure is provided by the
// result channel — main waits on it before handing off again.
type asyncFlusher struct {
	buf      *state.PlainStateBuffer
	db       kv.RwDB
	ctx      context.Context
	resultCh chan asyncFlushResult // capacity 1; receives the prev flush result
	inFlight bool
}

func newAsyncFlusher(buf *state.PlainStateBuffer, db kv.RwDB, ctx context.Context) *asyncFlusher {
	return &asyncFlusher{
		buf:      buf,
		db:       db,
		ctx:      ctx,
		resultCh: make(chan asyncFlushResult, 1),
	}
}

// waitPrev blocks until the previous in-flight flush (if any) finishes
// and reports its result + duration. Safe to call when no flush is
// pending — it returns (0, nil) immediately. Main calls this at the
// entry of each commit interval and before any code path that needs the
// in-flight data to be visible via MDBX (e.g. state-root verify).
func (f *asyncFlusher) waitPrev() (time.Duration, error) {
	if !f.inFlight {
		return 0, nil
	}
	res := <-f.resultCh
	f.inFlight = false
	if res.err != nil {
		return res.dur, fmt.Errorf("background flush failed: %w", res.err)
	}
	return res.dur, nil
}

// hand snapshots the active buffer (which atomically installs it as the
// in-flight snapshot for concurrent readers), then spawns a background
// goroutine that opens a fresh kv.RwTx, applies the snapshot, writes
// the progress marker, commits, and invalidates the LRU. The caller
// MUST have called waitPrev() first.
//
// blockNum is the highest block number reflected by the snapshot;
// it's persisted via WriteProgress inside the same tx so recovery
// observes a consistent (state, progress) pair.
func (f *asyncFlusher) hand(blockNum uint64) error {
	if f.inFlight {
		return errors.New("asyncFlusher: hand called while previous flush still pending — must waitPrev first")
	}
	snap := f.buf.SnapshotForFlush()
	f.inFlight = true
	go func() {
		t0 := time.Now()
		var err error
		var bgTx kv.RwTx
		bgTx, err = f.db.BeginRw(f.ctx)
		if err == nil {
			err = snap.ApplyTo(bgTx)
		}
		if err == nil {
			err = WriteProgress(bgTx, blockNum)
		}
		if err == nil {
			err = bgTx.Commit()
		} else if bgTx != nil {
			bgTx.Rollback()
		}
		if err == nil {
			// Refresh LRU with the just-flushed working set AFTER MDBX
			// commit. The snapshot's values ARE what landed in MDBX, so
			// they're authoritative — Put-not-Delete keeps the entire
			// flushed set hot for the next interval instead of forcing
			// every key back through MDBX on first re-access.
			f.buf.RefreshLRUForSnapshot(snap)
		}
		f.resultCh <- asyncFlushResult{err: err, dur: time.Since(t0)}
	}()
	return nil
}
