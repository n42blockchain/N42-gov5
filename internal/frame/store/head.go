// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package store

import "github.com/n42blockchain/N42/common/types"

// The unready index lists, in admission order, the frames that may still
// need their senders, so a reader of the oldest unready frames (the head of
// every gateway's pick order: N42_FRAME_HEAD_HEDGE, N42_FRAME_LOCAL_AFTER_MS)
// walks only those and not the whole store, which also holds the committed
// frames of the keep margin (thousands, under the read lock admission waits
// behind). It is off until EnableHeadIndex; with it off nothing changes.
//
// The index is lazy: an entry leaves once its frame is resolved (senders
// cached, committed, or gone) and every older entry has left too. A frame a
// written block pins stays listed (an abandoned fork releases it again) but
// is not reported.

// EnableHeadIndex turns the unready index on (idempotent). The frames
// already stored are indexed in arrival order.
func (s *Store) EnableHeadIndex() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hqOn {
		return
	}
	s.hqOn = true
	for _, h := range s.ring[s.head:] {
		if sl, ok := s.m[h]; ok && !headResolved(sl) {
			s.hq = append(s.hq, h)
		}
	}
}

// SetChangeNotify installs fn (nil removes it), called after every change
// that can move the head of the unready frames. fn must not block and must
// not call back into the store synchronously.
func (s *Store) SetChangeNotify(fn func()) {
	if fn == nil {
		s.onChange.Store(nil)
		return
	}
	s.onChange.Store(&fn)
}

func (s *Store) changed() {
	if fn := s.onChange.Load(); fn != nil {
		(*fn)()
	}
}

// headResolved reports whether a frame can leave the unready index for
// good: its senders are cached or a committed block holds it.
func headResolved(sl slot) bool { return sl.senders != nil || sl.commitAt != 0 }

// HeadFrame is one frame HeadUnready reports.
type HeadFrame struct {
	H   types.Hash
	Raw []byte
	At  int64 // admission time, unix nanoseconds
	// Running: a recovery owns it right now; Claimed: a block's import
	// recovers it.
	Running, Claimed bool
}

// HeadUnready calls fn, oldest first, for the stored frames of the unready
// index that no written block holds and whose senders are not cached, until
// fn returns false or maxVisit index entries were visited (<= 0: no
// bound). fn runs under the store's read lock: it must be cheap and must
// not call back into the store. Without EnableHeadIndex it reports nothing.
func (s *Store) HeadUnready(maxVisit int, fn func(HeadFrame) bool) {
	s.mu.RLock()
	trim := 0
	prefix := true
	for i, h := range s.hq[s.hqHead:] {
		if maxVisit > 0 && i >= maxVisit {
			break
		}
		sl, ok := s.m[h]
		if !ok || headResolved(sl) {
			if prefix {
				trim++
			}
			continue
		}
		prefix = false
		if sl.included() {
			continue
		}
		if !fn(HeadFrame{H: h, Raw: sl.Raw, At: sl.at, Running: sl.recov == recRunning, Claimed: sl.claimed}) {
			break
		}
	}
	s.mu.RUnlock()
	if trim >= headTrimMin {
		s.mu.Lock()
		s.trimHeadIndexLocked()
		s.mu.Unlock()
	}
}

// headTrimMin is the resolved prefix a reader trims under the write lock.
const headTrimMin = 64

// trimHeadIndexLocked drops the index's resolved prefix.
func (s *Store) trimHeadIndexLocked() {
	if !s.hqOn {
		return
	}
	for s.hqHead < len(s.hq) {
		if sl, ok := s.m[s.hq[s.hqHead]]; ok && !headResolved(sl) {
			break
		}
		s.hq[s.hqHead] = types.Hash{}
		s.hqHead++
	}
	if s.hqHead > 1024 && s.hqHead*2 > len(s.hq) {
		n := copy(s.hq, s.hq[s.hqHead:])
		clear(s.hq[n:])
		s.hq = s.hq[:n]
		s.hqHead = 0
	}
}

// HeadIndexLen returns the live length of the unready index (tests,
// observability).
func (s *Store) HeadIndexLen() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.hq) - s.hqHead
}
