// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"os"
	"sync/atomic"

	block "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// N42_CATCHUP_SKIP_APPLIED=1 (default off, r95a), the sync half.
//
// A hotstuff catch-up range starts at the committed head and comes back
// with blocks this node already applied: durably, or in the lag writer's
// write in flight (peers only serve blocks node0's executor has results
// for). Each one went through InsertChainAuthorized, which takes bc.lock
// and joins the write in flight before it finds the block known; the lag
// executor's own import then waited ~46 ms p50 for bc.lock (run 81, ~60
// ranges per leg).
//
// With the switch on, the range's leading blocks that are applied (the
// chain's applied-lineage probe, HasAppliedBlock) or held by
// the lag pipeline (its write in flight, or queued for its executor) are
// not inserted. An applied one is reported to the engine as an import
// would; a held one is left to the lag executor, which imports and reports
// it. Everything from the first block that is neither goes through the
// insert as today, and the chain re-checks under bc.lock
// (insertChainMode), so a block that turns applied between this check and
// the lock is still skipped as known.
var catchUpSkipAppliedOn = parseCatchUpSkipApplied(os.Getenv("N42_CATCHUP_SKIP_APPLIED"))

func parseCatchUpSkipApplied(v string) bool {
	switch v {
	case "1", "true", "TRUE", "on", "ON", "yes":
		return true
	}
	return false
}

// SetCatchUpSkipAppliedForTest switches N42_CATCHUP_SKIP_APPLIED (sync half)
// and returns the restore.
func SetCatchUpSkipAppliedForTest(on bool) func() {
	prev := catchUpSkipAppliedOn
	catchUpSkipAppliedOn = on
	return func() { catchUpSkipAppliedOn = prev }
}

// Catch-up range blocks not inserted because they were applied, or held by
// the lag pipeline, and those inserted (process-wide, since start). The
// last is counted with the switch off too.
var catchUpSkippedApplied, catchUpSkippedHeld, catchUpInserted atomic.Uint64

// CatchUpSkipStats returns the catch-up blocks skipped as applied, skipped
// as held by the lag pipeline, and inserted.
func CatchUpSkipStats() (applied, held, inserted uint64) {
	return catchUpSkippedApplied.Load(), catchUpSkippedHeld.Load(), catchUpInserted.Load()
}

// BlockArriving reports whether the direct-push handler holds hash right
// now: decoded, its availability check (and the queueing for the lag
// executor) not finished. hotstuff asserts for it on its BlockFetcher.
func (s *Service) BlockArriving(hash types.Hash) bool {
	_, ok := s.pushInflight.Load(hash)
	return ok
}

// hasAppliedLineage reports whether the chain has applied blk (its
// applied-lineage probe). Unlike blockApplied there is no fallback to body
// presence: a chain without the probe skips nothing.
func (s *Service) hasAppliedLineage(blk block.IBlock) bool {
	a, ok := s.cfg.chain.(interface {
		HasAppliedBlock(types.Hash, uint64) bool
	})
	return ok && a.HasAppliedBlock(blk.Hash(), blk.Number64().Uint64())
}

// lagPipelineHolds reports whether the chain's lag pipeline will import
// blk itself: it is the write in flight, or queued for the lag executor.
// Never at a height the sync importer owns (N42_SYNC_PIPELINE Range mode):
// the lag executor leaves those queued (BlockChain.lagSyncOwned), so a
// catch-up that skipped one would leave it to nobody.
func (s *Service) lagPipelineHolds(blk block.IBlock) bool {
	if s.syncOwnsHeight(blk.Number64().Uint64()) {
		return false
	}
	h, ok := s.cfg.chain.(interface {
		LagPipelineHolds(types.Hash, uint64) bool
	})
	return ok && h.LagPipelineHolds(blk.Hash(), blk.Number64().Uint64())
}

// trimCatchUpApplied drops the leading blocks of a fetched catch-up range
// that need no insert (see catchUpSkipAppliedOn). It returns the blocks
// still to insert (from the first block that is neither applied nor held,
// unchanged from there on), the applied blocks skipped, in order, and how
// many were skipped as held.
func (s *Service) trimCatchUpApplied(blocks []block.IBlock) (rest, applied []block.IBlock, held int) {
	for i, b := range blocks {
		switch {
		case s.hasAppliedLineage(b):
			applied = append(applied, b)
		case s.lagPipelineHolds(b):
			held++
		default:
			rest = blocks[i:]
		}
		if rest != nil {
			break
		}
	}
	catchUpSkippedApplied.Add(uint64(len(applied)))
	catchUpSkippedHeld.Add(uint64(held))
	return rest, applied, held
}

// notifyCatchUpApplied reports skipped applied blocks to the engine in
// order, as insertCatchUpBlocks reports each block it imported (the engine
// applies any reconfiguration a block crosses on that notification).
func (s *Service) notifyCatchUpApplied(applied []block.IBlock) {
	n := s.cfg.blockImportNotifier
	if n == nil {
		return
	}
	for _, b := range applied {
		n.NotifyBlockImported(b.Hash(), b.TxHash())
	}
}
