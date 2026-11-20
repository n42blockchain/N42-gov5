// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
)

// handoffEarly is clPipe.handoff with the early capture (the lag writer's
// N42_LAG_QCAP_EARLY): b's undo is taken and its capture started BEFORE
// the join of write b-1; the join's commit + eviction then run after the
// capture (JoinAfterCapture). A failed write b-1 aborts both windows and
// reloads (the capture is dropped). rowsOut receives b's rows.
func (p *clPipe) handoffEarly(check bool, rowsOut *clRows) (*qmdb.BlockUndo, bool) {
	undo := p.rc.TakeUndo()
	capture := p.rc.StartFlushToEarly(rowsOut, check)
	if w := p.inflight; w != nil {
		<-w.done
		p.inflight = nil
		if w.err != nil {
			p.rc.AbortFlushed() // waits for the capture (the lock)
			p.rc.TakeUndo()
			if err := p.db.Update(context.Background(), func(tx kv.RwTx) error {
				p.rc.SetCold(tx)
				err := p.rc.LoadFrom(tx)
				p.rc.SetCold(nil)
				return err
			}); err != nil {
				p.t.Fatal(err)
			}
			_, _ = capture.Wait()
			return nil, false
		}
		if p.rc.JoinAfterCapture(capture, true) {
			clAsyncTails++
		}
		defer func() {
			if _, second := capture.Early(); second {
				clSecond++
			}
		}()
	}
	p.startWrite(capture, rowsOut)
	return undo, true
}

// clWriteDelay holds every write before its transaction (tests).
var clWriteDelay time.Duration

// Early-capture counters of a run: second windows staged, joins whose
// commit + eviction ran as the capture's tail.
var clSecond, clAsyncTails int

// handoffSerial is clPipe.handoff recording b's rows into rowsOut.
func (p *clPipe) handoffSerial(rowsOut *clRows) (*qmdb.BlockUndo, bool) {
	if !p.join() {
		return nil, false
	}
	undo := p.rc.TakeUndo()
	capture := p.rc.StartFlushTo(rowsOut, true)
	p.startWrite(capture, rowsOut)
	return undo, true
}

func (p *clPipe) startWrite(capture *FlushCapture, rows *clRows) {
	defer func() { _, _ = capture.Wait() }() // the caller digests the rows
	w := &clWrite{done: make(chan struct{})}
	fail := p.failNext
	p.failNext = false
	p.inflight = w
	skip := p.skipNext
	p.skipNext = false
	go func() {
		defer close(w.done)
		if skip {
			w.err = errCLWriteFailed // the process died before the write began
			return
		}
		if d := clWriteDelay; d > 0 {
			time.Sleep(d) // the write in flight while the next capture runs
		}
		if _, err := capture.Wait(); err != nil {
			w.err = err
			return
		}
		w.err = p.db.Update(context.Background(), func(tx kv.RwTx) error {
			for _, r := range rows.rows {
				var err error
				if r.del {
					err = tx.Delete(r.table, r.key)
				} else {
					err = tx.Put(r.table, r.key, r.value)
				}
				if err != nil {
					return err
				}
			}
			if fail {
				return errCLWriteFailed
			}
			return nil
		})
	}()
}

func clRowsDigest(r *clRows) string {
	h := sha256.New()
	for _, x := range r.rows {
		fmt.Fprintf(h, "%s|%x|%x|%v;", x.table, x.key, x.value, x.del)
	}
	return fmt.Sprintf("%d:%x", len(r.rows), h.Sum(nil)[:8])
}

func clQMDBTables(t *testing.T, db kv.RwDB) string {
	t.Helper()
	h := sha256.New()
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		for _, tb := range []string{qmdb.EntryTable, qmdb.TwigTable, qmdb.LeavesTable, qmdb.MetaTable} {
			c, err := tx.Cursor(tb)
			if err != nil {
				return err
			}
			for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
				if err != nil {
					c.Close()
					return err
				}
				fmt.Fprintf(h, "%s|%x|%x;", tb, k, v)
			}
			c.Close()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// earlyPipeRun runs blocks through the pipeline with slow writes so the
// next block's capture overlaps the write in flight, failing write failAt
// (0: none) once, and returns every capture's rows in order, the final
// QMDB tables and the final root.
func earlyPipeRun(t *testing.T, early, check bool, lagK, blocks, failAt int, readers bool) (rows []string, tables string, root [32]byte, second int) {
	t.Helper()
	p := newCLPipe(t, true)
	p.rc.SetEvictLag(lagK)
	if readers {
		stop := make(chan struct{})
		wg := clReaders(t, p, clStates(blocks), stop, 4)
		defer func() { close(stop); wg.Wait() }()
	}
	handoff := func() (*clRows, bool) {
		r := &clRows{}
		var ok bool
		if early {
			_, ok = p.handoffEarly(check, r)
		} else {
			_, ok = p.handoffSerial(r)
		}
		return r, ok
	}
	failed := false
	for b := 1; b <= blocks; b++ {
		p.importRoot(b, clBlock(b, false))
		p.failNext = b == failAt && !failed
		if p.failNext {
			failed = true
		}
		r, ok := handoff()
		if !ok {
			// Write b-1 failed: the tree is at b-2; re-import b-1 and b.
			rows = append(rows, fmt.Sprintf("reload@%d", b))
			for rb := b - 1; rb <= b; rb++ {
				p.importRoot(rb, clBlock(rb, false))
				r2, ok := handoff()
				if !ok {
					t.Fatalf("re-import %d failed", rb)
				}
				rows = append(rows, clRowsDigest(r2))
			}
			continue
		}
		rows = append(rows, clRowsDigest(r))
	}
	if !p.join() {
		t.Fatal("last write failed")
	}
	if early {
		second = 1
	}
	return rows, clQMDBTables(t, p.db), p.rc.Root(), second
}

// TestEarlyCaptureSameRows: the early capture (second window, the join's
// commit + eviction after it) writes every block's rows exactly as the lean
// serial capture does, and the store and root end the same, with the evict
// lag 0 and 4, every twig-walk mode, a failed write of block N-1 while N's
// early capture exists, and lookups running beside it. =check asserts the
// window contiguity and the eviction choice at every join.
func TestEarlyCaptureSameRows(t *testing.T) {
	blocks := 16
	if testing.Short() {
		blocks = 9
	}
	for _, set := range []string{"", "1", "check"} {
		for _, lagK := range []int{0, 4} {
			for _, failAt := range []int{0, 6} {
				t.Run(fmt.Sprintf("set=%q/lag=%d/fail=%d", set, lagK, failAt), func(t *testing.T) {
					defer qmdb.SetFlushTwigSetForTest(set)()
					v0 := EarlyCaptureViolations()
					wantRows, wantTables, wantRoot, _ := earlyPipeRun(t, false, false, lagK, blocks, failAt, false)
					clSecond, clAsyncTails = 0, 0
					defer func() { clWriteDelay = 0 }()
					for i, check := range []bool{false, true} {
						// Slow writes: the capture overlaps the write in
						// flight; with a short delay some joins find the
						// capture still running (its tail commits).
						clWriteDelay = []time.Duration{2 * time.Millisecond, 200 * time.Microsecond}[i]
						gotRows, gotTables, gotRoot, _ := earlyPipeRun(t, true, check, lagK, blocks, failAt, failAt == 0)
						if len(gotRows) != len(wantRows) {
							t.Fatalf("check=%v: %d captures, serial %d", check, len(gotRows), len(wantRows))
						}
						for i := range wantRows {
							if gotRows[i] != wantRows[i] {
								t.Fatalf("check=%v: capture %d rows %s, serial %s", check, i, gotRows[i], wantRows[i])
							}
						}
						if gotTables != wantTables {
							t.Fatalf("check=%v: QMDB tables differ", check)
						}
						if gotRoot != wantRoot {
							t.Fatalf("check=%v: root differs", check)
						}
					}
					t.Logf("second windows %d, tails run by the capture %d", clSecond, clAsyncTails)
					if clSecond < blocks {
						t.Fatalf("only %d second windows", clSecond)
					}
					if v := EarlyCaptureViolations() - v0; v != 0 {
						t.Fatalf("%d early-capture violations", v)
					}
				})
			}
		}
	}
}

// TestEarlyCaptureStagesSecondWindow: with the write in flight the early
// capture stages a second window, the join's tail adopts the first and
// promotes the second, and a plain FlushTo is refused meanwhile.
func TestEarlyCaptureStagesSecondWindow(t *testing.T) {
	p := newCLPipe(t, true)
	p.importRoot(1, clBlock(1, false))
	r1 := &clRows{}
	if _, ok := p.handoffSerial(r1); !ok {
		t.Fatal("write 1")
	}
	hold := make(chan struct{})
	w := p.inflight
	w2 := &clWrite{done: make(chan struct{})}
	p.inflight = w2
	go func() { <-w.done; <-hold; w2.err = w.err; close(w2.done) }()
	p.importRoot(2, clBlock(2, false))
	r2 := &clRows{}
	capture := p.rc.StartFlushToEarly(r2, true)
	if _, err := capture.Wait(); err != nil {
		t.Fatal(err)
	}
	if early, second := capture.Early(); !early || !second {
		t.Fatalf("early %v second %v", early, second)
	}
	p.rc.readers.RLock()
	if !p.rc.stagedValid || !p.rc.stagedNextValid || !p.rc.t.SecondWindowStaged() {
		t.Fatal("two windows not staged")
	}
	p.rc.readers.RUnlock()
	if _, err := p.rc.FlushTo(&clRows{}); err == nil {
		t.Fatal("a plain FlushTo with two windows staged was accepted")
	}
	close(hold)
	<-w2.done
	p.inflight = nil
	if async := p.rc.JoinAfterCapture(capture, true); async {
		t.Fatal("the capture finished: the tail must run now")
	}
	p.rc.readers.RLock()
	if !p.rc.stagedValid || p.rc.stagedNextValid || p.rc.flushedThrough != capture.from {
		t.Fatalf("after the join: first %v second %v flushed %d from %d", p.rc.stagedValid, p.rc.stagedNextValid, p.rc.flushedThrough, capture.from)
	}
	p.rc.readers.RUnlock()
	if len(r2.rows) == 0 {
		t.Fatal("no rows captured")
	}
}

// Crash points of TestEarlyCaptureCrashPoints.
const (
	// crashPrevUncommitted: write N-1 never commits; block N's capture
	// (early: into the second window) exists.
	crashPrevUncommitted = iota
	// crashBeforeBegin: write N-1 committed (joined, N's window promoted);
	// write N never begins.
	crashBeforeBegin
	// crashMidWrite: write N dies inside its transaction, rows put.
	crashMidWrite
)

// crashRun runs blocks 1..n-1 through the pipeline, crashes at point while
// block n is in the hand-off, restarts on a fresh computer loaded from the
// store and imports n..total. It returns every capture's rows after the
// restart, the tables, the root and the reloaded root.
func crashRun(t *testing.T, early, check bool, lagK, n, total, point int) (rows []string, tables string, root, reloaded [32]byte) {
	t.Helper()
	p := newCLPipe(t, true)
	p.rc.SetEvictLag(lagK)
	handoff := func() bool {
		r := &clRows{}
		var ok bool
		if early {
			_, ok = p.handoffEarly(check, r)
		} else {
			_, ok = p.handoffSerial(r)
		}
		if ok {
			rows = append(rows, clRowsDigest(r))
		}
		return ok
	}
	for b := 1; b < n; b++ {
		p.importRoot(b, clBlock(b, false))
		p.failNext = point == crashPrevUncommitted && b == n-1
		if !handoff() {
			t.Fatalf("block %d", b)
		}
	}
	p.importRoot(n, clBlock(n, false))
	switch point {
	case crashPrevUncommitted:
		// Block n's capture starts (early: before the join); the join
		// finds write n-1 failed and the process dies there: nothing
		// recovers in memory.
		r := &clRows{}
		if early {
			p.rc.TakeUndo()
			c := p.rc.StartFlushToEarly(r, check)
			<-p.inflight.done
			_, _ = c.Wait()
		} else {
			<-p.inflight.done
		}
		if p.inflight.err == nil {
			t.Fatal("write n-1 committed")
		}
	case crashBeforeBegin, crashMidWrite:
		p.skipNext = point == crashBeforeBegin
		p.failNext = point == crashMidWrite
		if !handoff() {
			t.Fatal("write n-1 failed")
		}
		<-p.inflight.done
	}
	p.inflight = nil
	rows = rows[:0]
	for _, tx := range p.openTxs {
		tx.Rollback()
	}
	p.openTxs = nil

	// Restart: a fresh computer loaded from the store.
	rc := NewQMDBRootComputer()
	rc.EnableUndoRecording()
	rc.SetEvictLag(lagK)
	if err := p.db.Update(context.Background(), func(tx kv.RwTx) error {
		rc.SetCold(tx)
		err := rc.LoadFrom(tx)
		rc.SetCold(nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p.rc = rc
	reloaded = rc.Root()
	from := n
	if point == crashPrevUncommitted {
		from = n - 1
	}
	for b := from; b <= total; b++ {
		p.importRoot(b, clBlock(b, false))
		if !handoff() {
			t.Fatalf("after the restart: block %d", b)
		}
	}
	if !p.join() {
		t.Fatal("last write failed")
	}
	return rows, clQMDBTables(t, p.db), p.rc.Root(), reloaded
}

// TestEarlyCaptureCrashPoints: the process dies where the early capture
// moves work -- block N captured (second window) with write N-1 not
// committed; N-1 committed and N's window promoted with write N not begun;
// write N dead mid-transaction. Nothing of the capture is durable, so after
// a restart the store, the reloaded root and every later capture's rows
// equal the serial pipeline's crashed at the same point.
func TestEarlyCaptureCrashPoints(t *testing.T) {
	defer func() { clWriteDelay = 0 }()
	clWriteDelay = time.Millisecond
	const n, total = 6, 10
	for _, point := range []int{crashPrevUncommitted, crashBeforeBegin, crashMidWrite} {
		for _, lagK := range []int{0, 4} {
			t.Run(fmt.Sprintf("point=%d/lag=%d", point, lagK), func(t *testing.T) {
				v0 := EarlyCaptureViolations()
				wantRows, wantTables, wantRoot, wantReload := crashRun(t, false, false, lagK, n, total, point)
				for _, check := range []bool{false, true} {
					rows, tables, root, reload := crashRun(t, true, check, lagK, n, total, point)
					if reload != wantReload {
						t.Fatalf("check=%v: reloaded root differs", check)
					}
					if fmt.Sprint(rows) != fmt.Sprint(wantRows) {
						t.Fatalf("check=%v: captures after the restart differ:\n%v\n%v", check, rows, wantRows)
					}
					if tables != wantTables || root != wantRoot {
						t.Fatalf("check=%v: final store or root differs", check)
					}
				}
				if v := EarlyCaptureViolations() - v0; v != 0 {
					t.Fatalf("%d early-capture violations", v)
				}
			})
		}
	}
}
