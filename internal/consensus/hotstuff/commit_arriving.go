// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"os"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// N42_CATCHUP_SKIP_APPLIED=1 (default off, r95a), the consensus half.
//
// Run 81 (node0, every leg): about 60 commits per leg were reported as
// "committed block not executed locally" (number 0) and each one fired a
// catch-up range import. Traced on the log (block 14136952, B1): the block
// push arrived at t, the CommitQC at t+23 ms, and the block's availability
// check finished at t+36 ms and queued it for the lag executor. The commit
// was handled while the push handler still held the block in its
// availability check: neither availableHere (set at the end of that check)
// nor the stored header (written by the lag writer ~290 ms later) could see
// it. The catch-up then imported blocks the lag pipeline already held
// through InsertChainAuthorized, which takes bc.lock and joins the write in
// flight; the lag import waited ~46 ms p50 for bc.lock behind it.
//
// With the switch on, a commit whose block is still in the push handler
// (BlockArriving) defers the "executed here?" decision until that handler
// is done with it (bounded by commitArrivalWait): a block that passed its
// check is available and handled exactly like any available block (P2a);
// one that failed, or is still not here at the bound, is observed and
// caught up exactly as today. Only the moment of the observation moves; a
// genuinely missing block still gets its catch-up.
var catchUpSkipAppliedOn = os.Getenv("N42_CATCHUP_SKIP_APPLIED") == "1"

// SetCatchUpSkipAppliedForTest switches N42_CATCHUP_SKIP_APPLIED (consensus
// half) and returns the restore.
func SetCatchUpSkipAppliedForTest(on bool) func() {
	prev := catchUpSkipAppliedOn
	catchUpSkipAppliedOn = on
	return func() { catchUpSkipAppliedOn = prev }
}

// commitArrivalWait bounds how long a deferred commit observation waits for
// the push handler to finish the block (its availability check takes ~35 ms
// for a full block).
var commitArrivalWait = 2 * time.Second

// Commits whose observation waited for the block's arrival, and of those the
// ones that found the block available afterwards (no catch-up) and the ones
// that still needed a catch-up (process-wide, since start).
var commitArrivalDeferred, commitArrivalAvailable, commitArrivalCatchUp atomic.Uint64

// CommitArrivalStats returns the commits deferred behind the block's
// arrival, those that found it available, and those that caught up.
func CommitArrivalStats() (deferred, available, catchUp uint64) {
	return commitArrivalDeferred.Load(), commitArrivalAvailable.Load(), commitArrivalCatchUp.Load()
}

// observeCommitted checks that a committed block executes here and asks
// for a catch-up when it does not.
func (s *Service) observeCommitted(hash types.Hash) {
	switch {
	case s.availableHere(hash):
		// P2a: committed on availability; its execution runs behind in
		// the lag executor, which retries the canonical commit on import.
	case catchUpSkipAppliedOn && s.blockArriving(hash):
		// Still in its availability check here.
		s.deferCommittedObservation(hash)
	default:
		if applied, _, number := s.observeCommittedExecution(hash, true); !applied {
			s.requestCommittedCatchUp(hash, number)
		}
	}
}

// blockArriving reports whether the block fetcher (the sync layer) holds
// hash in its push handler right now: decoded, its availability check not
// finished.
func (s *Service) blockArriving(hash types.Hash) bool {
	a, ok := s.blockFetcher.(interface{ BlockArriving(types.Hash) bool })
	return ok && hash != (types.Hash{}) && a.BlockArriving(hash)
}

// deferCommittedObservation runs the committed block's execution check once
// the push handler is done with it; see catchUpSkipAppliedOn.
func (s *Service) deferCommittedObservation(hash types.Hash) {
	commitArrivalDeferred.Add(1)
	t0 := time.Now()
	go func() {
		deadline := time.NewTimer(commitArrivalWait)
		defer deadline.Stop()
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
	wait:
		for s.blockArriving(hash) {
			select {
			case <-s.ctx.Done():
				return
			case <-deadline.C:
				break wait
			case <-tick.C:
			}
		}
		if s.availableHere(hash) {
			commitArrivalAvailable.Add(1)
			log.Info("hotstuff: committed block arrived here, no catch-up", "hash", hash,
				"waitUs", time.Since(t0).Microseconds())
			return
		}
		if applied, _, number := s.observeCommittedExecution(hash, true); !applied {
			commitArrivalCatchUp.Add(1)
			s.requestCommittedCatchUp(hash, number)
		}
	}()
}
