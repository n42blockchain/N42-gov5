// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"os"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// N42_CATCHUP_SKIP_APPLIED=1 (default off, r95a), the chain half.
//
// Run 81 (node0): a hotstuff catch-up import (InsertChainAuthorized, also
// fetch-on-miss) of a block that is the lag writer's write in flight, or
// already applied, took bc.lock and joined that write before it found the
// block known; the lag executor's import of the next block waited ~46 ms
// p50 for bc.lock behind it (102 waits per leg, 3.3 ms per full block).
//
// With the switch on, insertChainMode -- for an import that is not the lag
// executor's -- returns at once, without joining the write in flight, when
// every block of the chain is either that write's block or on the durable
// applied lineage (HasAppliedBlock), on a commit-only (leader-driven) QMDB
// chain. That is exactly the outcome of the joined path for those blocks:
// once the write is durable each is a known block on the applied lineage,
// knownBlockNeedsAuthorizedReplay is false for it, it is ignored, and the
// call returns (len(chain), nil). The check runs under bc.lock, so it sees
// the same in-flight write the join would. A block that is not applied and
// not in flight is imported exactly as today.
//
// One difference, by construction harmless: when the write in flight later
// fails, the joined path returned its error; the skipped path returned
// success, and the failed write's recovery (recoverLagWrite, run by the lag
// loop or the next join) re-queues the block for the lag executor, which
// imports and reports it.
var catchUpSkipAppliedOn = parseOptIn(os.Getenv("N42_CATCHUP_SKIP_APPLIED"))

// SetCatchUpSkipAppliedForTest switches N42_CATCHUP_SKIP_APPLIED (chain half)
// and returns the restore.
func SetCatchUpSkipAppliedForTest(on bool) func() {
	prev := catchUpSkipAppliedOn
	catchUpSkipAppliedOn = on
	return func() { catchUpSkipAppliedOn = prev }
}

// Non-lag imports that returned without joining the write in flight, and
// those that joined it (process-wide, since start; the second is counted
// with the switch off too).
var insertSkippedJoin, insertJoinedWrite atomic.Uint64

// InsertJoinStats returns the non-lag imports that skipped joining the
// write in flight (their blocks applied or in that write) and those that
// joined it.
func InsertJoinStats() (skipped, joined uint64) {
	return insertSkippedJoin.Load(), insertJoinedWrite.Load()
}

// LagPipelineHolds reports whether the lag pipeline imports hash itself:
// it is the block of the write in flight, or it waits in (or is being
// imported by) the lag executor. Any goroutine.
func (bc *BlockChain) LagPipelineHolds(hash types.Hash, number uint64) bool {
	if j := bc.lagWritePending(hash); j != nil && j.number == number {
		return true
	}
	return bc.LagImportQueued(hash)
}

// chainAppliedOrInFlightLocked reports whether every block of chain is the
// write in flight's block or on the durable applied lineage (bc.lock held).
func (bc *BlockChain) chainAppliedOrInFlightLocked(chain []block.IBlock) bool {
	j := bc.lagWrite.inflight
	if j == nil || len(chain) == 0 || !bc.qmdbEnabled || bc.ChainDB == nil || !bc.canonicalByCommitOnly() {
		return false
	}
	for _, b := range chain {
		if b == nil {
			return false
		}
		n := b.Number64().Uint64()
		h := b.Hash()
		if h == j.hash && n == j.number {
			continue
		}
		if !bc.HasAppliedBlock(h, n) {
			return false
		}
	}
	return true
}
