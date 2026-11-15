// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package store

import (
	"math/rand/v2"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// Frame admission is bounded by the store lock (r93): every frame takes the
// store's write lock several times (admission, sender recovery begin and
// finish, attestations) and its read lock several more (resolve, build),
// and the builder walks the whole ring under the read lock. Under load the
// lock hands off one waiter at a time, each a scheduler wakeup, and the
// FRM1 admissions, serialised behind it, set the node's frame arrival rate.
// Three opt-in switches, default off, each independent of the others:
//
//   - N42_FRAME_ADMIT_COMBINE: an FRM1 admission (the duplicate check, the
//     per-gateway in-order check and the insert, in that order) is one
//     operation queued to a write combiner: whoever finds the combiner idle
//     applies every queued operation under one write lock, in queue order,
//     and wakes the waiters in parallel. The replies are those of a serial
//     admission in queue order.
//   - N42_FRAME_STORE_BATCH: admission recovery skips the store for frames
//     another source answers, claims through the combiner and finishes
//     asynchronously through it; attestations cache their senders through
//     it; block resolution and the build read raws and senders under one
//     read lock per block; the change notify runs outside the lock.
//   - N42_FRAME_SCAN_FAST: the builder scans start after the committed
//     prefix of the ring and run their callback on a snapshot taken under
//     the read lock, not under it.

// Switch names.
const (
	EnvFrameAdmitCombine = "N42_FRAME_ADMIT_COMBINE"
	EnvFrameStoreBatch   = "N42_FRAME_STORE_BATCH"
	EnvFrameScanFast     = "N42_FRAME_SCAN_FAST"
)

var admitCombineOn, storeBatchOn, scanFastOn atomic.Bool

func init() {
	admitCombineOn.Store(parseOptIn(os.Getenv(EnvFrameAdmitCombine)))
	storeBatchOn.Store(parseOptIn(os.Getenv(EnvFrameStoreBatch)))
	scanFastOn.Store(parseOptIn(os.Getenv(EnvFrameScanFast)))
}

func parseOptIn(v string) bool {
	switch v {
	case "1", "true", "TRUE", "on", "ON", "yes":
		return true
	}
	return false
}

// AdmitCombineOn reports N42_FRAME_ADMIT_COMBINE.
func AdmitCombineOn() bool { return admitCombineOn.Load() }

// StoreBatchOn reports N42_FRAME_STORE_BATCH.
func StoreBatchOn() bool { return storeBatchOn.Load() }

// ScanFastOn reports N42_FRAME_SCAN_FAST.
func ScanFastOn() bool { return scanFastOn.Load() }

func setSwitch(b *atomic.Bool, on bool) func() {
	prev := b.Swap(on)
	return func() { b.Store(prev) }
}

// SetAdmitCombineForTest switches N42_FRAME_ADMIT_COMBINE and returns the restore.
func SetAdmitCombineForTest(on bool) func() { return setSwitch(&admitCombineOn, on) }

// SetStoreBatchForTest switches N42_FRAME_STORE_BATCH and returns the restore.
func SetStoreBatchForTest(on bool) func() { return setSwitch(&storeBatchOn, on) }

// SetScanFastForTest switches N42_FRAME_SCAN_FAST and returns the restore.
func SetScanFastForTest(on bool) func() { return setSwitch(&scanFastOn, on) }

// WaitStat accumulates sampled lock waits; Take reads and resets it.
type WaitStat struct {
	sum, n, max atomic.Int64
}

// Observe records one wait.
func (w *WaitStat) Observe(d time.Duration) {
	ns := int64(d)
	w.sum.Add(ns)
	w.n.Add(1)
	for {
		m := w.max.Load()
		if ns <= m || w.max.CompareAndSwap(m, ns) {
			return
		}
	}
}

// Take returns the mean and max wait and the sample count since the last
// Take, and resets them.
func (w *WaitStat) Take() (mean, max time.Duration, n int64) {
	n = w.n.Swap(0)
	sum := w.sum.Swap(0)
	max = time.Duration(w.max.Swap(0))
	if n > 0 {
		mean = time.Duration(sum / n)
	}
	return mean, max, n
}

// WaitSampleMask: one acquisition in WaitSampleMask+1 is timed.
const WaitSampleMask = 63

// Sampled reports whether this acquisition is timed (1 in 64).
func Sampled() bool { return rand.Uint32()&WaitSampleMask == 0 }

// lockSampled takes the write lock, timing the wait of 1 in 64
// acquisitions into the admission lock-wait stat.
func (s *Store) lockSampled() {
	if !Sampled() {
		s.mu.Lock()
		return
	}
	t0 := time.Now()
	s.mu.Lock()
	s.admitLockWait.Observe(time.Since(t0))
}

// TakeAdmitLockWait returns the sampled store write-lock wait of admissions
// (FRM1 Admit, or the combiner's lock for its batch) since the last call.
func (s *Store) TakeAdmitLockWait() (mean, max time.Duration, n int64) {
	return s.admitLockWait.Take()
}

// Write combiner.

type opKind uint8

const (
	opAdmit opKind = iota
	opBegin
	opFinish
	opSetSenders
)

type wop struct {
	kind opKind
	wait bool // a caller waits on done
	h    types.Hash
	// opAdmit
	e   Entry
	gw  types.Address
	seq uint64
	// opBegin
	skipClaimed bool
	// opFinish / opSetSenders
	t  RecoverTicket
	ss *Senders
	// results
	res InOrderResult
	tk  RecoverTicket
	// done: false = applied, true = the receiver leads the combiner now.
	done chan bool
}

var wopPool = sync.Pool{New: func() any { return &wop{done: make(chan bool, 1)} }}

func getWop(k opKind) *wop {
	o := wopPool.Get().(*wop)
	o.kind = k
	return o
}

func putWop(o *wop) {
	*o = wop{done: o.done}
	wopPool.Put(o)
}

type combiner struct {
	mu    sync.Mutex
	q     []*wop
	spare []*wop
	busy  bool
}

// combineMaxRounds bounds the batches one leader applies before it hands
// the combiner to a waiting caller, so no caller leads forever.
const combineMaxRounds = 2

// combineSync queues o and returns once it is applied.
func (s *Store) combineSync(o *wop) {
	o.wait = true
	c := &s.wc
	c.mu.Lock()
	c.q = append(c.q, o)
	if c.busy {
		c.mu.Unlock()
		if lead := <-o.done; !lead {
			return
		}
		c.mu.Lock()
	} else {
		c.busy = true
	}
	s.leadLocked(o)
}

// combineAsync queues ops and returns without waiting for them, applying
// them (and whatever else is queued) when the combiner is idle.
func (s *Store) combineAsync(ops ...*wop) {
	c := &s.wc
	c.mu.Lock()
	c.q = append(c.q, ops...)
	if c.busy {
		c.mu.Unlock()
		return
	}
	c.busy = true
	s.leadLocked(nil)
}

// leadLocked applies queued batches; c.mu is held on entry and released on
// return. self (nil for async) is the leader's own op: it is applied in the
// first batch and not signalled.
func (s *Store) leadLocked(self *wop) {
	c := &s.wc
	for round := 1; ; round++ {
		if len(c.q) == 0 {
			c.busy = false
			c.mu.Unlock()
			return
		}
		batch := c.q
		c.q, c.spare = c.spare[:0], nil
		c.mu.Unlock()
		s.applyOps(batch, self)
		clear(batch)
		c.mu.Lock()
		if c.spare == nil {
			c.spare = batch[:0]
		}
		if len(c.q) == 0 {
			c.busy = false
			c.mu.Unlock()
			return
		}
		if round >= combineMaxRounds {
			for _, o := range c.q {
				if o.wait {
					c.mu.Unlock()
					o.done <- true
					return
				}
			}
			if round >= combineAsyncRounds {
				// Only async ops keep coming: a fresh goroutine leads on, so
				// the caller (a recovery worker, an attestation reader) is
				// not held for long.
				c.mu.Unlock()
				go func() {
					c.mu.Lock()
					s.leadLocked(nil)
				}()
				return
			}
		}
	}
}

// combineAsyncRounds bounds the batches a leader applies when no caller
// waits to take over.
const combineAsyncRounds = 16

// applyOps applies a batch under one write lock, in queue order, then
// wakes recovery waiters, notifies once and releases the callers.
func (s *Store) applyOps(batch []*wop, self *wop) {
	s.lockSampled()
	notify := false
	for _, o := range batch {
		switch o.kind {
		case opAdmit:
			o.res = s.admitInOrderLocked(o.h, o.e, o.gw, o.seq)
			notify = notify || o.res.Result == Stored
		case opBegin:
			o.tk = s.beginRecoverLocked(o.h, o.skipClaimed)
		case opFinish:
			s.finishRecoverLocked(o.t, o.ss, false)
			notify = true
		case opSetSenders:
			if s.setSendersLocked(o.h, o.ss) {
				notify = true
			}
		}
	}
	s.mu.Unlock()
	for _, o := range batch {
		if o.kind == opFinish {
			close(o.t.ch)
		}
	}
	if notify {
		s.changed()
	}
	for _, o := range batch {
		switch {
		case o == self:
		case o.wait:
			o.done <- false
		default:
			putWop(o)
		}
	}
}

// InOrderResult is the outcome of AdmitInOrder.
type InOrderResult struct {
	Result
	// Known: the frame was stored already (checked first, nothing counted
	// by the store). OutOfOrder: refused as Busy, its seq beyond the
	// gateway's last accepted + 1 (S90).
	Known, OutOfOrder bool
}

// AdmitInOrder is FRM1 admission as one serial step (N42_FRAME_ADMIT_COMBINE):
// a frame already stored is Known; after the gateway's first stored frame,
// a seq beyond the last stored + 1 is OutOfOrder; otherwise Admit without
// bypass, and a stored frame advances the gateway's last seq. It runs
// through the write combiner: the replies are those of a serial admission
// in queue order.
func (s *Store) AdmitInOrder(h types.Hash, e Entry, gw types.Address, seq uint64) InOrderResult {
	o := getWop(opAdmit)
	o.h, o.e, o.gw, o.seq = h, e, gw, seq
	s.combineSync(o)
	r := o.res
	putWop(o)
	return r
}

func (s *Store) admitInOrderLocked(h types.Hash, e Entry, gw types.Address, seq uint64) InOrderResult {
	if _, ok := s.m[h]; ok {
		return InOrderResult{Result: Duplicate, Known: true}
	}
	last, seen := s.lastSeq[gw]
	if seen && seq > last+1 {
		return InOrderResult{Result: Busy, OutOfOrder: true}
	}
	if int64(len(e.Raw)) > s.budget {
		s.rejected.Add(1)
		return InOrderResult{Result: TooLarge}
	}
	r := s.admitLocked(h, e, false)
	if r == Stored && (!seen || seq > last) {
		if s.lastSeq == nil {
			s.lastSeq = make(map[types.Address]uint64)
		}
		s.lastSeq[gw] = seq
	}
	return InOrderResult{Result: r}
}

// BeginRecoverCombined is BeginRecover through the write combiner.
func (s *Store) BeginRecoverCombined(h types.Hash, skipClaimed bool) RecoverTicket {
	o := getWop(opBegin)
	o.h, o.skipClaimed = h, skipClaimed
	s.combineSync(o)
	t := o.tk
	putWop(o)
	return t
}

// FinishRecoverAsync is FinishRecover through the write combiner: it may
// return before ss is cached; the ticket's waiters are woken once it is.
func (s *Store) FinishRecoverAsync(t RecoverTicket, ss *Senders) {
	if !t.Own || t.ch == nil {
		return
	}
	o := getWop(opFinish)
	o.t, o.ss = t, ss
	s.combineAsync(o)
}

// SetSendersAsync is SetSenders for each hs[i] (ss[i] nil: skipped)
// through the write combiner, applied under one lock with whatever else is
// queued; it may return before they are cached.
func (s *Store) SetSendersAsync(hs []types.Hash, ss []*Senders) {
	ops := make([]*wop, 0, len(hs))
	for i, h := range hs {
		if ss[i] == nil {
			continue
		}
		o := getWop(opSetSenders)
		o.h, o.ss = h, ss[i]
		ops = append(ops, o)
	}
	if len(ops) > 0 {
		s.combineAsync(ops...)
	}
}

// Batched reads.

// GetBatch sets raws[i] to the frame stored under hs[i] (nil when not
// stored) under one read lock and returns how many are missing. Hits and
// misses are counted as by Get.
func (s *Store) GetBatch(hs []types.Hash, raws [][]byte) (missing int) {
	s.mu.RLock()
	for i, h := range hs {
		if sl, ok := s.m[h]; ok {
			raws[i] = sl.Raw
		} else {
			raws[i] = nil
			missing++
		}
	}
	s.mu.RUnlock()
	s.hits.Add(uint64(len(hs) - missing))
	s.misses.Add(uint64(missing))
	return missing
}

// SendersBatch sets out[i] to the cached senders of hs[i] (nil when none or
// not stored) under one read lock.
func (s *Store) SendersBatch(hs []types.Hash, out []*Senders) {
	s.mu.RLock()
	for i, h := range hs {
		if sl, ok := s.m[h]; ok {
			out[i] = sl.senders
		} else {
			out[i] = nil
		}
	}
	s.mu.RUnlock()
}

// RecoverInfoBatch is RecoverInfoOf for each of hs under one read lock.
func (s *Store) RecoverInfoBatch(hs []types.Hash, out []RecoverInfo) {
	now := time.Now().UnixNano()
	s.mu.RLock()
	for i, h := range hs {
		sl, ok := s.m[h]
		if !ok {
			out[i] = RecoverInfo{}
			continue
		}
		out[i] = RecoverInfo{Stored: true, Age: time.Duration(now - sl.at),
			Ready: sl.senders != nil, Running: sl.recov == recRunning, Claimed: sl.claimed}
	}
	s.mu.RUnlock()
}

// Scan cursor (N42_FRAME_SCAN_FAST).

// advanceFillLowLocked moves the scan cursor past the ring's prefix of
// committed or dropped frames. A committed frame never becomes uncommitted,
// so the prefix only grows (ring compaction resets the cursor).
func (s *Store) advanceFillLowLocked() {
	i := max(s.fillLow, s.head)
	for i < len(s.ring) {
		if sl, ok := s.m[s.ring[i]]; ok && sl.commitAt == 0 {
			break
		}
		i++
	}
	s.fillLow = i
}

// scanStartLocked is where a scan of uncommitted frames starts.
func (s *Store) scanStartLocked() int {
	if !scanFastOn.Load() {
		return s.head
	}
	return min(max(s.fillLow, s.head), len(s.ring))
}

type scanItem struct {
	h      types.Hash
	e      Entry
	ready  bool
	pinned bool
}

var scanBufPool = sync.Pool{New: func() any { b := make([]scanItem, 0, 1024); return &b }}

// snapshotUncommitted copies the uncommitted frames (fillable only: no
// written block holds them) with their readiness, oldest first, under the
// read lock.
func (s *Store) snapshotUncommitted(grace time.Duration, fillableOnly bool) *[]scanItem {
	recovering := s.recoverer.Load() != nil
	cutoff := time.Now().Add(-grace).UnixNano()
	bp := scanBufPool.Get().(*[]scanItem)
	buf := (*bp)[:0]
	s.mu.RLock()
	for _, h := range s.ring[s.scanStartLocked():] {
		sl, ok := s.m[h]
		if !ok || sl.commitAt != 0 || (fillableOnly && sl.pendAt != 0) {
			continue
		}
		buf = append(buf, scanItem{h: h, e: sl.Entry,
			ready: !recovering || sl.senders != nil || sl.at <= cutoff, pinned: sl.pendAt != 0})
	}
	s.mu.RUnlock()
	*bp = buf
	return bp
}

func releaseScanBuf(bp *[]scanItem) {
	clear(*bp)
	*bp = (*bp)[:0]
	scanBufPool.Put(bp)
}
