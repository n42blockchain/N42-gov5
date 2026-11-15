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

package store

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// LocalHurrier is a Hurrier that can also recover a frame on this node
// although another source (P8b attestations) is expected to answer it: a
// block builder held on that frame needs it now (N42_FRAME_HELD_WAIT_MS).
// HurryLocal must not block; it reports whether it took h.
type LocalHurrier interface {
	HurryLocal(h types.Hash, raw []byte) bool
}

// HurryLocal is Hurry for frames this node recovers itself even when their
// senders are expected from attestations (LocalHurrier). The filter is
// Hurry's: frames with cached senders, a running recovery or a block's
// claim are left alone. It returns how many frames the recoverer took.
func (s *Store) HurryLocal(hs []types.Hash) int {
	box := s.recoverer.Load()
	if box == nil || len(hs) == 0 {
		return 0
	}
	hr, ok := box.r.(LocalHurrier)
	if !ok {
		return 0
	}
	return s.hurry(hs, hr.HurryLocal)
}

// AgedFrame is a frame AgedUnready returns.
type AgedFrame struct {
	H   types.Hash
	Raw []byte
	At  int64 // admission time, unix nanoseconds
}

// AgedUnready returns, oldest first, up to max stored frames admitted at or
// before cutoff (unix nanoseconds) that no block holds, that have no cached
// senders, no running recovery and no block's claim, and for which skip
// (optional; called under the store's read lock, it must not call back into
// the store) is false (N42_FRAME_LOCAL_AFTER_MS). The scan stops at the
// first frame admitted after cutoff: the ring is in admission order.
//
// With the unready index on (EnableHeadIndex) only the frames that may
// still need senders are visited; otherwise the walk passes every stored
// frame older than cutoff, the committed frames of the keep margin
// included, under the read lock.
func (s *Store) AgedUnready(cutoff int64, max int, skip func(types.Hash) bool) []AgedFrame {
	if max <= 0 {
		return nil
	}
	var out []AgedFrame
	s.mu.RLock()
	if s.hqOn {
		s.mu.RUnlock()
		s.HeadUnready(0, func(f HeadFrame) bool {
			if f.At > cutoff {
				return false
			}
			if f.Running || f.Claimed || (skip != nil && skip(f.H)) {
				return true
			}
			out = append(out, AgedFrame{H: f.H, Raw: f.Raw, At: f.At})
			return len(out) < max
		})
		return out
	}
	defer s.mu.RUnlock()
	for _, h := range s.ring[s.head:] {
		sl, ok := s.m[h]
		if !ok {
			continue
		}
		if sl.at > cutoff {
			break
		}
		if sl.included() || sl.senders != nil || sl.recov != recNone || sl.claimed || (skip != nil && skip(h)) {
			continue
		}
		out = append(out, AgedFrame{H: h, Raw: sl.Raw, At: sl.at})
		if len(out) >= max {
			break
		}
	}
	return out
}

// HasSenders reports whether frame h is stored with cached senders.
func (s *Store) HasSenders(h types.Hash) bool {
	s.mu.RLock()
	sl, ok := s.m[h]
	s.mu.RUnlock()
	return ok && sl.senders != nil
}

// RecoverInfo describes a stored frame's sender recovery (observability).
type RecoverInfo struct {
	Stored  bool
	Age     time.Duration // since admission
	Ready   bool          // senders cached
	Running bool          // a recovery owns it right now
	Claimed bool          // a block's import recovers it
}

// RecoverInfoOf returns frame h's recovery state.
func (s *Store) RecoverInfoOf(h types.Hash) RecoverInfo {
	s.mu.RLock()
	sl, ok := s.m[h]
	s.mu.RUnlock()
	if !ok {
		return RecoverInfo{}
	}
	return RecoverInfo{Stored: true, Age: time.Duration(time.Now().UnixNano() - sl.at),
		Ready: sl.senders != nil, Running: sl.recov == recRunning, Claimed: sl.claimed}
}

// NReadyBuckets is the number of readiness latency buckets.
const NReadyBuckets = 12

// ReadyBucketsMs are the readiness latency buckets' upper bounds in
// milliseconds; the last bucket (index NReadyBuckets-1) is unbounded.
var ReadyBucketsMs = [NReadyBuckets - 1]int64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000}

const (
	readyAttested = 0
	readyLocal    = 1
)

// readyLatency counts frames by admission -> senders cached.
type readyLatency struct {
	b [2][NReadyBuckets]atomic.Uint64
}

func (r *readyLatency) observe(src int, admittedAt int64) {
	if admittedAt == 0 {
		return
	}
	ms := (time.Now().UnixNano() - admittedAt) / int64(time.Millisecond)
	i := 0
	for i < len(ReadyBucketsMs) && ms >= ReadyBucketsMs[i] {
		i++
	}
	r.b[src][i].Add(1)
}

func (r *readyLatency) snapshot() (att, loc [NReadyBuckets]uint64) {
	for i := range att {
		att[i] = r.b[readyAttested][i].Load()
		loc[i] = r.b[readyLocal][i].Load()
	}
	return att, loc
}

// FormatReadyBuckets renders cumulative readiness counts as
// "<1ms:n <2ms:n ... >=2000ms:n" (empty buckets left out).
func FormatReadyBuckets(c [NReadyBuckets]uint64) string {
	var sb strings.Builder
	for i, n := range c {
		if n == 0 {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteByte(' ')
		}
		if i < len(ReadyBucketsMs) {
			fmt.Fprintf(&sb, "<%dms:%d", ReadyBucketsMs[i], n)
		} else {
			fmt.Fprintf(&sb, ">=%dms:%d", ReadyBucketsMs[len(ReadyBucketsMs)-1], n)
		}
	}
	return sb.String()
}
