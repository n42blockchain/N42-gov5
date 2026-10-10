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
// height the depth applies to (blocks below it keep k=1, so a chain that ran
// at depth 1 can be continued, and re-imported, at depth 2).
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
	// EnvVoteOnAvailability turns the availability vote on.
	EnvVoteOnAvailability = "N42_VOTE_ON_AVAILABILITY"
	// MaxDeferredDepth bounds k.
	MaxDeferredDepth = 8
)

var (
	lagOnce      sync.Once
	lagDepth     atomic.Int64
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

// DeferredDepthFrom returns the first height the configured depth applies to.
func DeferredDepthFrom() uint64 {
	loadLagEnv()
	return lagFrom.Load()
}

// DeferredDepthAt returns the depth that applies to a block at number: the
// configured k from N42_DEFERRED_DEPTH_FROM on, 1 below it.
func DeferredDepthAt(number uint64) int {
	loadLagEnv()
	if number < lagFrom.Load() {
		return 1
	}
	return int(lagDepth.Load())
}

// LagMode reports a configured depth above 1: execution runs behind
// consensus and a leader proposes without executing its block.
func LagMode() bool { return DeferredDepth() > 1 }

// VoteOnAvailability reports N42_VOTE_ON_AVAILABILITY=1 with
// N42_FRAME_BLOCKS=1.
func VoteOnAvailability() bool {
	loadLagEnv()
	return availability.Load()
}

// SetDeferredDepthForTest overrides the depth and its first height and
// returns the restore function.
func SetDeferredDepthForTest(k int, from uint64) func() {
	loadLagEnv()
	pk, pf := lagDepth.Load(), lagFrom.Load()
	if k < 1 {
		k = 1
	}
	lagDepth.Store(int64(k))
	lagFrom.Store(from)
	return func() { lagDepth.Store(pk); lagFrom.Store(pf) }
}

// SetVoteOnAvailabilityForTest overrides the availability vote switch and
// returns the restore function.
func SetVoteOnAvailabilityForTest(on bool) func() {
	loadLagEnv()
	prev := availability.Load()
	availability.Store(on)
	return func() { availability.Store(prev) }
}
