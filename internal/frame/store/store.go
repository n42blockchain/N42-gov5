// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

// Package store holds received frames keyed by FrameHash, in arrival
// order, under a byte budget with bounded admission
// (docs/QS_MULTI_MILLION_DESIGN.md, S89):
//
//   - A frame no committed block holds is never evicted. A written but
//     uncommitted block (any fork) pins its frames too; they become fillable
//     again when the committed head passes that height without them.
//   - A frame is evicted only once a block holding it has been committed and
//     the committed head has moved KeepBlocks (N42_FRAME_KEEP_BLOCKS, default
//     64) past it. Eviction is eager, at commit time, and leaves a tombstone
//     so a resend is answered as a duplicate.
//   - When a new frame does not fit the budget, admission answers Busy and
//     stores nothing. Frames a node needs to vote on or import a proposal
//     (FRQ1 pulls) and the leader's own local frames bypass the budget; both
//     are bounded by one block.
//
// Each frame is one pointer-free []byte, so the garbage collector marks it
// without scanning its contents; the per-frame overhead on the scanned heap
// is one map entry and one ring slot.
package store

import (
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// DefaultBudget is the default byte budget (4 GiB).
const DefaultBudget int64 = 4 << 30

// EnvBudget names the environment variable that overrides the budget.
const EnvBudget = "N42_FRAME_STORE_BYTES"

// DefaultKeepBlocks is the default number of committed blocks a frame is
// kept for after the block holding it was committed.
const DefaultKeepBlocks uint64 = 64

// EnvKeepBlocks names the environment variable that overrides KeepBlocks.
const EnvKeepBlocks = "N42_FRAME_KEEP_BLOCKS"

// Stats is a snapshot of the store's counters.
type Stats struct {
	Frames        int
	Bytes         int64
	Budget        int64
	KeepBlocks    uint64
	CommittedHead uint64
	Pending       int // frames held by written, uncommitted blocks
	Committed     int // frames held by committed blocks, inside the keep margin
	Puts          uint64
	Duplicates    uint64
	Rejected      uint64 // larger than the whole budget
	Busy          uint64 // refused: budget full and nothing evictable
	Bypassed      uint64 // admitted over the budget (pulls, local frames)
	Evicted       uint64 // committed frames dropped after the keep margin
	EvictedBytes  uint64
	Released      uint64 // pinned by an abandoned fork, fillable again
	Hits          uint64
	Misses        uint64
	Removed       uint64 // unincluded frames dropped by Remove
	// Sender recovery at admission (P2a): frames whose senders are cached,
	// frames admitted and not yet recovered, and frames recovered since
	// start (zero without a recoverer).
	WithSenders    int
	RecoverPending int64
	Recovered      uint64
}

// Senders are a frame's transaction senders, recovered at admission and
// aligned with frame.DecodeTxs's output (undecodable entries dropped). A
// zero address marks an entry whose signature did not recover; it is left
// to execution. Signer is the signer the recovery ran under: a block whose
// own signer differs re-derives (transaction.Sender checks the memo's
// signer), so a cached sender can never change a verdict.
type Senders struct {
	Signer transaction.Signer
	Addrs  []types.Address
}

// Recoverer recovers the senders of admitted frames in the background and
// hands them back through SetSenders. Submit must not block.
type Recoverer interface {
	Submit(h types.Hash, raw []byte)
	Pending() int64
	Recovered() uint64
}

// Entry is one stored frame with the metadata computed at admission.
type Entry struct {
	Raw []byte
	Gas uint64 // sum of the transactions' gas limits (frame.GasEstimate)
	Txs int    // transaction count from the header
}

// Result is the outcome of Admit.
type Result uint8

const (
	Stored    Result = iota // admitted
	Duplicate               // already stored, or tombstoned (a block took it)
	Busy                    // the budget is full and nothing is evictable
	TooLarge                // the frame alone exceeds the budget
)

// slot is a stored frame and its inclusion state.
type slot struct {
	Entry
	pendAt   uint64 // highest height of a written, uncommitted block holding it (0 = none)
	commitAt uint64 // height of the committed block holding it (0 = none)
	senders  *Senders
	// claimed: a block resolved this frame, so that block's import recovers
	// its senders and admission recovery skips it.
	claimed bool
	// recov is the frame's sender recovery state (P8a): exactly one path
	// (admission, a block's sender job, the import) owns the recovery of a
	// frame; the others wait on recWait or take the result.
	recov   uint8
	recWait chan struct{}
	// at is the admission time (unix nanoseconds), for the builder's
	// readiness grace (ScanFillableReady).
	at int64
}

// Sender recovery states of a stored frame.
const (
	recNone    uint8 = iota // nobody has recovered it
	recRunning              // an owner is recovering it
	recDone                 // senders cached
)

func (sl *slot) included() bool { return sl.pendAt != 0 || sl.commitAt != 0 }

type committedFrame struct {
	h types.Hash
	n uint64
}

// tombstoneCap bounds the set of dropped frame hashes remembered so a
// resent frame that is already in a block is not admitted again.
const tombstoneCap = 1 << 18

// Store is safe for concurrent use.
type Store struct {
	mu     sync.RWMutex
	budget int64
	keep   uint64
	used   int64
	m      map[types.Hash]slot
	ring   []types.Hash // arrival order; ring[head:] are live or dropped
	head   int

	// pend indexes pinned frames by the height of the block that pinned
	// them, so a commit can release the ones an abandoned fork held.
	pend map[uint64][]types.Hash
	// cq lists committed frames in commit order (heights ascending); its
	// head is the next frame to evict once the keep margin has passed.
	cq        []committedFrame
	cqHead    int
	committed uint64 // committed head height (valid when haveHead)
	haveHead  bool
	nPend     int // stored frames with pendAt != 0 and commitAt == 0
	nCommit   int // stored frames with commitAt != 0
	nSenders  int // stored frames with cached senders

	recoverer atomic.Pointer[recovererBox]

	// Frames dropped after a block took them, or by Remove: a Put of one of
	// these is answered as a duplicate. FIFO-bounded by tombstoneCap.
	tomb     map[types.Hash]struct{}
	tombRing []types.Hash
	tombHead int

	puts, dups, rejected, busy, bypassed, evicted, evictedBytes, released, hits, misses, removed atomic.Uint64
}

// New returns a store with the given byte budget (<= 0 selects the default)
// and the default keep margin.
func New(budget int64) *Store {
	return NewWithKeep(budget, DefaultKeepBlocks)
}

// NewWithKeep returns a store with the given byte budget and keep margin
// in committed blocks (budget <= 0 selects the default budget).
func NewWithKeep(budget int64, keep uint64) *Store {
	if budget <= 0 {
		budget = DefaultBudget
	}
	return &Store{
		budget: budget,
		keep:   keep,
		m:      make(map[types.Hash]slot),
		pend:   make(map[uint64][]types.Hash),
		tomb:   make(map[types.Hash]struct{}),
	}
}

// NewFromEnv returns a store sized by N42_FRAME_STORE_BYTES and
// N42_FRAME_KEEP_BLOCKS, or the defaults.
func NewFromEnv() *Store {
	var b int64
	if v := os.Getenv(EnvBudget); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			b = n
		}
	}
	keep := DefaultKeepBlocks
	if v := os.Getenv(EnvKeepBlocks); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			keep = n
		}
	}
	return NewWithKeep(b, keep)
}

// Put stores frame under h. It returns false when h is already present,
// the budget is full, or the frame alone exceeds the budget. The store
// keeps frame as given; the caller must not modify it afterwards.
func (s *Store) Put(h types.Hash, frame []byte) bool {
	return s.PutEntry(h, Entry{Raw: frame})
}

// PutEntry is Put with the admission metadata the block builder reads.
func (s *Store) PutEntry(h types.Hash, e Entry) bool {
	return s.Admit(h, e, false) == Stored
}

type recovererBox struct{ r Recoverer }

// SetRecoverer attaches the background sender recovery every admitted frame
// is handed to (nil detaches).
func (s *Store) SetRecoverer(r Recoverer) {
	if r == nil {
		s.recoverer.Store(nil)
		return
	}
	s.recoverer.Store(&recovererBox{r: r})
}

// SetSenders caches the recovered senders of the stored frame h. It reports
// false when h is no longer stored.
func (s *Store) SetSenders(h types.Hash, ss *Senders) bool {
	if ss == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sl, ok := s.m[h]
	if !ok {
		return false
	}
	if sl.senders == nil {
		s.nSenders++
	}
	sl.senders = ss
	if sl.recov == recNone {
		// A running owner finishes (and wakes its waiters) on its own.
		sl.recov = recDone
	}
	s.m[h] = sl
	return true
}

// RecoverTicket is the answer of BeginRecover. Exactly one of Senders,
// Wait and Own is set for a stored frame; none for a frame not stored.
type RecoverTicket struct {
	H       types.Hash
	Stored  bool
	Senders *Senders        // already recovered
	Wait    <-chan struct{} // another owner is recovering it; closed when done
	Own     bool            // the caller recovers it and must call FinishRecover
	ch      chan struct{}
}

// BeginRecover claims the sender recovery of frame h for the caller (P8a:
// one recovery per frame per node). With skipClaimed (admission recovery)
// a frame a block has claimed is neither owned nor waited for: the block's
// job recovers it.
func (s *Store) BeginRecover(h types.Hash, skipClaimed bool) RecoverTicket {
	s.mu.Lock()
	defer s.mu.Unlock()
	sl, ok := s.m[h]
	if !ok {
		return RecoverTicket{H: h}
	}
	t := RecoverTicket{H: h, Stored: true}
	switch {
	case sl.senders != nil:
		t.Senders = sl.senders
	case sl.recov == recRunning:
		t.Wait = sl.recWait
	case skipClaimed && sl.claimed:
	default:
		sl.recov = recRunning
		sl.recWait = make(chan struct{})
		s.m[h] = sl
		t.Own, t.ch = true, sl.recWait
	}
	return t
}

// FinishRecover ends an owned recovery: ss (nil when the recovery failed
// or was abandoned) is cached with the frame and every waiter is woken. A
// frame that left the store meanwhile only wakes its waiters.
func (s *Store) FinishRecover(t RecoverTicket, ss *Senders) {
	if !t.Own || t.ch == nil {
		return
	}
	s.mu.Lock()
	if sl, ok := s.m[t.H]; ok && sl.recWait == t.ch {
		if ss != nil {
			if sl.senders == nil {
				s.nSenders++
			}
			sl.senders = ss
		}
		if sl.senders != nil {
			sl.recov = recDone
		} else {
			sl.recov = recNone
		}
		sl.recWait = nil
		s.m[t.H] = sl
	}
	s.mu.Unlock()
	close(t.ch)
}

// Claim marks the stored frames among hs as resolved by a block: the
// block's own sender job recovers them, so admission recovery skips them
// instead of recovering them a second time.
func (s *Store) Claim(hs []types.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range hs {
		if sl, ok := s.m[h]; ok && !sl.claimed {
			sl.claimed = true
			s.m[h] = sl
		}
	}
}

// NeedsSenders reports whether admission recovery should recover frame h:
// it is stored, has no cached senders and no block has claimed it.
func (s *Store) NeedsSenders(h types.Hash) bool {
	s.mu.RLock()
	sl, ok := s.m[h]
	s.mu.RUnlock()
	return ok && sl.senders == nil && !sl.claimed && sl.recov == recNone
}

// Senders returns the cached senders of frame h, or nil.
func (s *Store) Senders(h types.Hash) *Senders {
	s.mu.RLock()
	sl, ok := s.m[h]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	return sl.senders
}

// Admit stores e under h. With bypass the byte budget is not applied:
// frames needed to vote on or import a proposal (FRQ1 pulls) and the
// leader's own local frames, each bounded by one block. Without it a frame
// that does not fit is refused as Busy: frames are only ever evicted by
// commits (Commit), never to make room.
func (s *Store) Admit(h types.Hash, e Entry, bypass bool) Result {
	size := int64(len(e.Raw))
	if size > s.budget && !bypass {
		s.rejected.Add(1)
		return TooLarge
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[h]; ok {
		s.dups.Add(1)
		return Duplicate
	}
	if _, ok := s.tomb[h]; ok {
		s.dups.Add(1)
		return Duplicate
	}
	if s.used+size > s.budget {
		if !bypass {
			s.busy.Add(1)
			return Busy
		}
		s.bypassed.Add(1)
	}
	s.m[h] = slot{Entry: e, at: time.Now().UnixNano()}
	s.ring = append(s.ring, h)
	s.used += size
	s.puts.Add(1)
	if box := s.recoverer.Load(); box != nil {
		// Under the store lock only to keep Submit ordered with the insert;
		// Submit never blocks.
		box.r.Submit(h, e.Raw)
	}
	return Stored
}

// Include pins the stored frames among hs to a written block at number:
// they are no longer fillable and are not evicted. A block at or below the
// committed head that was not committed is a late fork and pins nothing.
func (s *Store) Include(hs []types.Hash, number uint64) {
	if number == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.haveHead && number <= s.committed {
		return
	}
	var pinned []types.Hash
	for _, h := range hs {
		sl, ok := s.m[h]
		if !ok || sl.commitAt != 0 || sl.pendAt >= number {
			continue
		}
		if sl.pendAt == 0 {
			s.nPend++
		}
		sl.pendAt = number
		s.m[h] = sl
		pinned = append(pinned, h)
	}
	if len(pinned) > 0 {
		s.pend[number] = append(s.pend[number], pinned...)
	}
}

// Commit records that the block at number holding hs was committed and
// moves the committed head to number: frames pinned only by blocks at or
// below it that were not committed (abandoned forks) become fillable again,
// and committed frames KeepBlocks below it are evicted. Commits must come
// in ascending height; a commit at or below the current head only marks
// frames. Frames named but not stored are tombstoned so a resend is
// refused.
func (s *Store) Commit(number uint64, hs []types.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range hs {
		sl, ok := s.m[h]
		if !ok {
			s.tombstoneLocked(h)
			continue
		}
		if sl.commitAt != 0 {
			continue
		}
		if sl.pendAt != 0 {
			s.nPend--
		}
		s.nCommit++
		sl.commitAt, sl.pendAt = number, 0
		s.m[h] = sl
		s.cq = append(s.cq, committedFrame{h: h, n: number})
	}
	if s.haveHead && number <= s.committed {
		return
	}
	s.committed, s.haveHead = number, true
	for n, list := range s.pend {
		if n > number {
			continue
		}
		for _, h := range list {
			sl, ok := s.m[h]
			if !ok || sl.commitAt != 0 || sl.pendAt == 0 || sl.pendAt > number {
				continue
			}
			sl.pendAt = 0
			s.m[h] = sl
			s.nPend--
			s.released.Add(1)
		}
		delete(s.pend, n)
	}
	s.evictCommittedLocked()
}

// evictCommittedLocked drops committed frames whose block is KeepBlocks or
// more below the committed head.
func (s *Store) evictCommittedLocked() {
	dropped := false
	for s.cqHead < len(s.cq) {
		c := s.cq[s.cqHead]
		if c.n+s.keep > s.committed {
			break
		}
		s.cq[s.cqHead] = committedFrame{}
		s.cqHead++
		sl, ok := s.m[c.h]
		if !ok || sl.commitAt != c.n {
			continue
		}
		s.used -= int64(len(sl.Raw))
		delete(s.m, c.h)
		s.nCommit--
		if sl.senders != nil {
			s.nSenders--
		}
		s.evicted.Add(1)
		s.evictedBytes.Add(uint64(len(sl.Raw)))
		s.tombstoneLocked(c.h)
		dropped = true
	}
	if s.cqHead > 1024 && s.cqHead*2 > len(s.cq) {
		n := copy(s.cq, s.cq[s.cqHead:])
		s.cq = s.cq[:n]
		s.cqHead = 0
	}
	if dropped {
		s.reclaimRingLocked()
	}
}

// tombstoneLocked remembers h as taken by a block or dropped.
func (s *Store) tombstoneLocked(h types.Hash) {
	if _, ok := s.tomb[h]; ok {
		return
	}
	s.tomb[h] = struct{}{}
	s.tombRing = append(s.tombRing, h)
	if len(s.tombRing)-s.tombHead > tombstoneCap {
		delete(s.tomb, s.tombRing[s.tombHead])
		s.tombRing[s.tombHead] = types.Hash{}
		s.tombHead++
		if s.tombHead > 1024 && s.tombHead*2 > len(s.tombRing) {
			n := copy(s.tombRing, s.tombRing[s.tombHead:])
			s.tombRing = s.tombRing[:n]
			s.tombHead = 0
		}
	}
}

// reclaimRingLocked drops the ring's dead prefix and, once dead slots
// dominate the live part, rebuilds the ring from the live frames in order.
func (s *Store) reclaimRingLocked() {
	for s.head < len(s.ring) {
		if _, ok := s.m[s.ring[s.head]]; ok {
			break
		}
		s.ring[s.head] = types.Hash{}
		s.head++
	}
	live := len(s.m)
	if span := len(s.ring) - s.head; span > 1024 && span > 2*live {
		out := s.ring[:0]
		for _, h := range s.ring[s.head:] {
			if _, ok := s.m[h]; ok {
				out = append(out, h)
			}
		}
		for i := len(out); i < len(s.ring); i++ {
			s.ring[i] = types.Hash{}
		}
		s.ring = out
		s.head = 0
		return
	}
	if s.head > 1024 && s.head*2 > len(s.ring) {
		n := copy(s.ring, s.ring[s.head:])
		s.ring = s.ring[:n]
		s.head = 0
	}
}

// Get returns the frame stored under h.
func (s *Store) Get(h types.Hash) ([]byte, bool) {
	s.mu.RLock()
	e, ok := s.m[h]
	s.mu.RUnlock()
	if ok {
		s.hits.Add(1)
	} else {
		s.misses.Add(1)
	}
	return e.Raw, ok
}

// GetEntry returns the frame and its admission metadata.
func (s *Store) GetEntry(h types.Hash) (Entry, bool) {
	s.mu.RLock()
	e, ok := s.m[h]
	s.mu.RUnlock()
	return e.Entry, ok
}

// Included reports whether h is stored and held by a written block.
func (s *Store) Included(h types.Hash) bool {
	s.mu.RLock()
	sl, ok := s.m[h]
	s.mu.RUnlock()
	return ok && sl.included()
}

// Scan calls fn for each stored frame in arrival order, oldest first,
// until fn returns false. It holds the read lock for the walk, so fn must
// be cheap and must not call back into the store.
func (s *Store) Scan(fn func(h types.Hash, e Entry) bool) {
	s.scan(false, fn)
}

// ScanFillable is Scan over the frames no written block holds: the
// frames a block builder may take, oldest first.
func (s *Store) ScanFillable(fn func(h types.Hash, e Entry) bool) {
	s.scan(true, fn)
}

// ScanFillableReady is ScanFillable that also tells fn whether the frame
// is ready for a block (P10): its senders are cached (recovered here or
// accepted from attestations), or no recoverer is attached (nothing would
// ever cache them), or it was admitted at least grace ago (its senders
// are late; the block recovers them). A block of ready frames executes
// with no sender recovery left at block time.
func (s *Store) ScanFillableReady(grace time.Duration, fn func(h types.Hash, e Entry, ready bool) bool) {
	recovering := s.recoverer.Load() != nil
	cutoff := time.Now().Add(-grace).UnixNano()
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, h := range s.ring[s.head:] {
		sl, ok := s.m[h]
		if !ok || sl.included() {
			continue
		}
		ready := !recovering || sl.senders != nil || sl.at <= cutoff
		if !fn(h, sl.Entry, ready) {
			return
		}
	}
}

func (s *Store) scan(fillable bool, fn func(h types.Hash, e Entry) bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, h := range s.ring[s.head:] {
		sl, ok := s.m[h]
		if !ok {
			continue // dropped; its ring slot is reclaimed later
		}
		if fillable && sl.included() {
			continue
		}
		if !fn(h, sl.Entry) {
			return
		}
	}
}

// Remove drops frames no written block holds (the leader's unused local
// frames) and remembers their hashes so a resend is not admitted again.
// Frames a block holds are left to Commit's eviction.
func (s *Store) Remove(hs []types.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range hs {
		if sl, ok := s.m[h]; ok {
			if sl.included() {
				continue
			}
			s.used -= int64(len(sl.Raw))
			delete(s.m, h)
			s.removed.Add(1)
			if sl.senders != nil {
				s.nSenders--
			}
		}
		s.tombstoneLocked(h)
	}
	s.reclaimRingLocked()
}

// Has reports whether h is stored.
func (s *Store) Has(h types.Hash) bool {
	s.mu.RLock()
	_, ok := s.m[h]
	s.mu.RUnlock()
	return ok
}

// Len returns the number of stored frames.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.m)
}

// CommittedHead returns the committed head height and whether one was seen.
func (s *Store) CommittedHead() (uint64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.committed, s.haveHead
}

// Stats returns a snapshot of the counters.
func (s *Store) Stats() Stats {
	s.mu.RLock()
	st := Stats{Frames: len(s.m), Bytes: s.used, Budget: s.budget, KeepBlocks: s.keep, CommittedHead: s.committed,
		Pending: s.nPend, Committed: s.nCommit, WithSenders: s.nSenders}
	s.mu.RUnlock()
	if box := s.recoverer.Load(); box != nil {
		st.RecoverPending = box.r.Pending()
		st.Recovered = box.r.Recovered()
	}
	st.Puts = s.puts.Load()
	st.Duplicates = s.dups.Load()
	st.Rejected = s.rejected.Load()
	st.Busy = s.busy.Load()
	st.Bypassed = s.bypassed.Load()
	st.Evicted = s.evicted.Load()
	st.EvictedBytes = s.evictedBytes.Load()
	st.Released = s.released.Load()
	st.Hits = s.hits.Load()
	st.Misses = s.misses.Load()
	st.Removed = s.removed.Load()
	return st
}
