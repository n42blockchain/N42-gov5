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

package block

import (
	"os"
	"strconv"
	"sync"
	"sync/atomic"
)

// Execution off the consensus path (docs/QS_MULTI_MILLION_DESIGN.md P2a).
//
// Deferred execution depth k: under deferred execution a header at height N
// carries the executed state root, receipts root, bloom and gas used of
// block N-k (k=1 is the original rule: the parent's). With k=2 a node votes
// on N without having executed N-1, and may propose N once it has executed
// N-2. The depth is a consensus rule: every node of the chain must run with
// the same N42_DEFERRED_DEPTH, and N42_DEFERRED_DEPTH_FROM names the first
// height the depth applies to. Blocks below it keep the previous depth
// N42_DEFERRED_DEPTH_PREV (default 1), so a chain that ran at depth 1 can be
// continued, and re-imported, at depth 2, and one that ran at depth 2 at
// depth 3 (N42_DEFERRED_DEPTH_PREV=2).
//
// Boundary rule. Write k(n) = DeferredDepthAt(n): PREV for n < FROM, K from
// FROM on. Header n carries the executed result of block t(n) = n - k(n)
// (genesis when that is below 0), and every consumer -- header validation,
// the availability vote, the leader gate (LagReady), the leader build, the
// gas-used bound and the reseeded-result lookup -- resolves t(n) through the
// per-height k(n), never through the configured K alone. Across the
// boundary t(FROM-1) = FROM-1-PREV and t(FROM) = FROM-K. With K = PREV+1
// (the 2 -> 3 switch) both are FROM-1-PREV: headers FROM-1 and FROM carry
// the same block's result (repeated, identical, checked twice), and from
// FROM+1 on t advances one block per height again, so no block's result is
// skipped. The leader gate for FROM therefore asks for nothing beyond what
// FROM-1 already needed: the transition hands the executor one block of
// slack. A larger step (K > PREV+1) makes t go back K-PREV-1 blocks once,
// still never skipping one. A smaller depth (K < PREV) would skip the
// results of FROM-PREV .. FROM-K-1 entirely, so PREV is clamped to K.
// FROM must be above every block already stored under the old rule: a
// stored header at n >= FROM was validated against n-PREV, and the new rule
// would re-check it against n-K.
//
// Vote on availability (N42_VOTE_ON_AVAILABILITY=1, with N42_FRAME_BLOCKS=1):
// a follower checks a frame block's static header fields, its frame hash
// list and that it holds every frame, and votes without executing.

const (
	// EnvDeferredDepth is the deferred execution depth k (default 1).
	EnvDeferredDepth = "N42_DEFERRED_DEPTH"
	// EnvDeferredDepthFrom is the first block number the depth applies to
	// (default 0: every deferred block).
	EnvDeferredDepthFrom = "N42_DEFERRED_DEPTH_FROM"
	// EnvDeferredDepthPrev is the depth of the blocks below
	// N42_DEFERRED_DEPTH_FROM (default 1, clamped to [1, N42_DEFERRED_DEPTH]).
	EnvDeferredDepthPrev = "N42_DEFERRED_DEPTH_PREV"
	// EnvVoteOnAvailability turns the availability vote on.
	EnvVoteOnAvailability = "N42_VOTE_ON_AVAILABILITY"
	// MaxDeferredDepth bounds k.
	MaxDeferredDepth = 8
)

var (
	lagOnce      sync.Once
	lagDepth     atomic.Int64
	lagPrev      atomic.Int64
	lagFrom      atomic.Uint64
	availability atomic.Bool
)

func loadLagEnv() {
	lagOnce.Do(func() {
		k := int64(1)
		if v, err := strconv.Atoi(os.Getenv(EnvDeferredDepth)); err == nil && v >= 1 {
			k = int64(v)
			if k > MaxDeferredDepth {
				k = MaxDeferredDepth
			}
		}
		lagDepth.Store(k)
		prev := int64(1)
		if v, err := strconv.Atoi(os.Getenv(EnvDeferredDepthPrev)); err == nil && v >= 1 {
			prev = int64(v)
		}
		lagPrev.Store(clampPrev(prev, k))
		if v, err := strconv.ParseUint(os.Getenv(EnvDeferredDepthFrom), 10, 64); err == nil {
			lagFrom.Store(v)
		}
		availability.Store(os.Getenv(EnvVoteOnAvailability) == "1" && os.Getenv("N42_FRAME_BLOCKS") == "1")
	})
}

// DeferredDepth returns the configured depth k (>= 1).
func DeferredDepth() int {
	loadLagEnv()
	return int(lagDepth.Load())
}

// clampPrev bounds the previous depth to [1, k]: a depth that decreases at
// the boundary would leave blocks whose result no header carries.
func clampPrev(prev, k int64) int64 {
	if prev < 1 {
		return 1
	}
	if prev > k {
		return k
	}
	return prev
}

// DeferredDepthPrev returns the depth of the blocks below DeferredDepthFrom.
func DeferredDepthPrev() int {
	loadLagEnv()
	return int(lagPrev.Load())
}

// DeferredDepthFrom returns the first height the configured depth applies to.
func DeferredDepthFrom() uint64 {
	loadLagEnv()
	return lagFrom.Load()
}

// DeferredDepthAt returns the depth that applies to a block at number: the
// configured k from N42_DEFERRED_DEPTH_FROM on, N42_DEFERRED_DEPTH_PREV
// (default 1) below it. See the boundary rule above.
func DeferredDepthAt(number uint64) int {
	loadLagEnv()
	if number < lagFrom.Load() {
		return int(lagPrev.Load())
	}
	return int(lagDepth.Load())
}

// LagMode reports a configured depth above 1 (the current one or, below
// N42_DEFERRED_DEPTH_FROM, the previous one; the previous never exceeds the
// current): execution runs behind consensus and a leader proposes without
// executing its block.
func LagMode() bool { return DeferredDepth() > 1 }

// VoteOnAvailability reports N42_VOTE_ON_AVAILABILITY=1 with
// N42_FRAME_BLOCKS=1.
func VoteOnAvailability() bool {
	loadLagEnv()
	return availability.Load()
}

// SetDeferredDepthForTest overrides the depth and its first height (depth 1
// below it) and returns the restore function.
func SetDeferredDepthForTest(k int, from uint64) func() {
	return SetDeferredDepthTransitionForTest(1, k, from)
}

// SetDeferredDepthTransitionForTest overrides the previous depth, the depth
// and its first height and returns the restore function.
func SetDeferredDepthTransitionForTest(prev, k int, from uint64) func() {
	loadLagEnv()
	pk, pp, pf := lagDepth.Load(), lagPrev.Load(), lagFrom.Load()
	if k < 1 {
		k = 1
	}
	lagDepth.Store(int64(k))
	lagPrev.Store(clampPrev(int64(prev), int64(k)))
	lagFrom.Store(from)
	return func() { lagDepth.Store(pk); lagPrev.Store(pp); lagFrom.Store(pf) }
}

// SetVoteOnAvailabilityForTest overrides the availability vote switch and
// returns the restore function.
func SetVoteOnAvailabilityForTest(on bool) func() {
	loadLagEnv()
	prev := availability.Load()
	availability.Store(on)
	return func() { availability.Store(prev) }
}
