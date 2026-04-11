// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// async_flush.go — background PlainStateBuffer flusher.
//
// At every commit interval the executor used to do FlushToMDBX +
// tx.Commit synchronously, blocking the EVM for ~25 seconds at the
// 11M-block scale. asyncFlusher hides that latency by running the
// flush in a dedicated goroutine while the executor continues
// processing the next commit interval with a fresh write buffer +
// MDBX tx.
//
// Reader correctness during the in-flight window is provided by the
// PlainStateBuffer.inFlight pointer: BufferedPlainStateReader checks
// active buffer → in-flight snapshot → LRU → MDBX. The in-flight
// snapshot stays pinned until the NEXT commit interval, so the main
// thread's mid-window MDBX tx (which has a stale snapshot vs. the
// background commit) never returns wrong data — keys missing from
// MDBX(staleTx) are still in the snapshot.
//
// Single in-flight: at most one buffer is being flushed at any time.
// At each commit interval the main thread waits for the previous flush
// to complete BEFORE handing off the new one. Under steady-state
// (exec_time >= flush_time) this wait is zero.
//
// Crash consistency: the output freezer (acctcs/storcs/witness) is
// still flushed synchronously at every commit interval, so on crash
// the output may be ahead of PlainState by up to one commit interval.
// Recovery replays the missing range from the V2 changesets via the
// rebuild-state forward path (no EVM needed).

package ethel

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/state"
)

// asyncFlushResult is sent on the result channel when the bg flush
// completes. The duration is captured for the progress log.
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
	db       kv.RwDB              // background goroutine opens its own RwTx via this
	ctx      context.Context      // for BeginRw
	resultCh chan asyncFlushResult // capacity 1; receives the prev flush result
	pending  bool                  // true iff a flush is currently in flight
	lastDur  time.Duration         // most recent successful flush duration
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
// and reports its result. Safe to call when no flush is pending — it
// returns nil immediately. Main calls this at the entry of each commit
// interval AND before any code path that needs the in-flight data to
// be visible via MDBX (e.g. state-root verify).
func (f *asyncFlusher) waitPrev() error {
	if !f.pending {
		return nil
	}
	res := <-f.resultCh
	f.pending = false
	f.lastDur = res.dur
	if res.err != nil {
		return fmt.Errorf("background flush failed: %w", res.err)
	}
	return nil
}

// lastFlushDuration returns the most recent successful flush duration
// (zero if no flush has run yet). It's logged in the progress line so
// the cost of bg flushes is visible.
func (f *asyncFlusher) lastFlushDuration() time.Duration {
	return f.lastDur
}

// pendingFlush reports whether a background flush is currently in flight.
func (f *asyncFlusher) pendingFlush() bool { return f.pending }

// hand snapshots the active buffer, installs it as in-flight, and
// spawns a background goroutine that opens a fresh kv.RwTx, applies
// the snapshot, writes the progress marker, commits, and invalidates
// the LRU. The caller MUST have called waitPrev() first.
//
// blockNum is the highest block number reflected by the snapshot;
// it's persisted via WriteProgress inside the same tx so recovery
// observes a consistent (state, progress) pair.
func (f *asyncFlusher) hand(blockNum uint64) error {
	if f.pending {
		return errors.New("asyncFlusher: hand called while previous flush still pending — must waitPrev first")
	}
	snap := f.buf.SnapshotForFlush()
	f.buf.SetInFlight(snap)
	f.pending = true
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
			// Invalidate LRU for the dirty set AFTER MDBX commit so
			// subsequent reads via the new (post-commit) tx land on
			// fresh MDBX values, not stale cache.
			f.buf.InvalidateLRUForSnapshot(snap)
		}
		f.resultCh <- asyncFlushResult{err: err, dur: time.Since(t0)}
	}()
	return nil
}
