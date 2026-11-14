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
//
// Isolated root computers and rewind bookkeeping for speculative miner builds.

package internal

import (
	"fmt"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/envswitch"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// NewMinerRootComputer returns an ISOLATED root computer for speculative block
// building. The miner/worker assembles candidate blocks that may never be
// committed (another validator's proposal may win the HotStuff round), so it
// must NOT mutate the live commitment tree. For QMDB this reloads a fresh twig
// forest from the DB at the current head — bc.qmdbRootComputer is untouched and
// there is no concurrent-mutation race. The instance is discarded with the
// block. Returns nil when no tree-based commitment is active (the miner then
// falls back to the default MPT state root). tx must be a read tx at the parent
// head; the QMDB DB state tracks the head because writeBlockWithState flushes
// per block.
// BuildParallel runs the builder's candidate list through Block-STM on ibs
// (lenient: failed candidates are dropped) and returns the survivors with
// their receipts. See StateProcessor.BuildParallel.
func (bc *BlockChain) BuildParallel(header *block.Header, txs []*transaction.Transaction, ibs *state.IntraBlockState, blockHashFunc func(n uint64) types.Hash) (included []*transaction.Transaction, receipts block.Receipts, usedGas uint64, failed int, err error) {
	sp, ok := bc.process.(*StateProcessor)
	if !ok || sp == nil {
		return nil, nil, 0, 0, ErrParallelNotApplicable
	}
	run, err := sp.BuildParallel(header, txs, ibs, blockHashFunc)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	return run.Included, run.Receipts, run.UsedGas, run.Failed, nil
}

// voidMinerRootTrust makes the builder's persistent speculative computer
// rebuild from the entry log on its next use. Its fast reload trusts its
// index below a cursor for THIS store's layout; an unwind or a failed-block
// revert rewrites entries below that cursor, and a trusted reload over the
// new layout yields a root the followers cannot reproduce. Round 35m, node2
// leading four views in a row: a stale seal at 13881479, a branch switch to
// the lowest-hash sibling, and its next speculative build proposed 13881480
// with a root six followers rejected (state root mismatch, zero
// transactions). The full rebuild is 10-23 s at 13.9M blocks, so only the
// mutating unwind voids it: the failed-block revert fires on routine paths
// (35m's relaunch: 48 voids in four minutes, every build a full reload,
// one block a minute).
func (bc *BlockChain) voidMinerRootTrust(reason string) {
	bc.minerRCMu.Lock()
	defer bc.minerRCMu.Unlock()
	if bc.minerRC != nil {
		bc.minerRC.VoidIndexTrust()
		log.Warn("miner speculative tree: index trust voided; next build rebuilds", "reason", reason)
	}
}

// minerAdoptAppends gates NewMinerRootComputer's own-block fast path; off by
// default until a fleet round has read it (QS_BLOCK_TIME_BUDGET, round 35zf).
var minerAdoptAppends = envswitch.Is1("N42_MINER_ADOPT_APPENDS", envswitch.Perf, "NewMinerRootComputer's own-block fast path.")

// buildStallDiagEnabled gates S11's diagnostic mutex-wait timers on bc.lock
// (AlignAppliedBranch, InsertChainAuthorized) and minerRCMu
// (NewMinerRootComputer). Read once at start-up, same pattern as
// minerAdoptAppends above. Off by default: the extra time.Now() pair around
// each Lock() is skipped entirely, so a fleet round that has not opted in
// pays nothing. See docs/QS_BLOCK_TIME_BUDGET.md 6by/S11 and the matching
// switch in internal/miner/build_stall_watchdog.go.
var buildStallDiagEnabled = envswitch.Is1("N42_BUILD_STALL_DIAG", envswitch.Diag, "Diagnostic mutex-wait timers on the build path and its watchdog.")

// TakeBuildStallLockWait returns and resets the bc.lock wait time accumulated
// by AlignAppliedBranch/InsertChainAuthorized calls since the last call to
// this method. Diagnostic only: always zero unless N42_BUILD_STALL_DIAG=1.
func (bc *BlockChain) TakeBuildStallLockWait() time.Duration {
	return time.Duration(bc.buildStallLockWaitNs.Swap(0))
}

// TakeBuildStallRootLockWait is TakeBuildStallLockWait for minerRCMu (the
// lock NewMinerRootComputer shares with the startup pre-warm).
func (bc *BlockChain) TakeBuildStallRootLockWait() time.Duration {
	return time.Duration(bc.buildStallRootLockWaitNs.Swap(0))
}

// MinerAdoptAppends reports whether the miner tree keeps its own appends
// (N42_MINER_ADOPT_APPENDS=1), which is also what lets a speculative build
// chain on an own unwritten block; the worker snapshots each sealed block's
// post-state only in that mode.
func MinerAdoptAppends() bool { return minerAdoptAppends }

// NewMinerRootComputer returns the isolated computer a leader build seals
// its state root on, or (nil, nil) when the chain does not commit with QMDB.
// A reload failure is returned as an error rather than swallowed: the caller
// used to seal on the default root instead, a block no follower accepts and
// one whose stale write later panicked the worker (round 35r).
func (bc *BlockChain) NewMinerRootComputer(tx kv.Tx, parentRoot types.Hash) (state.RootComputer, error) {
	if !bc.qmdbEnabled {
		return nil, nil
	}
	// PERSISTENT speculative computer: a fresh instance pays a full index
	// rescan (5.9M point reads, ~5s IO-bound — invisible on CPU profiles) on
	// EVERY build, which alone consumed most of the 6s view window and kept
	// the network in its slow, timeout-riddled steady state. Reusing one
	// instance rides the sealed-twig O(meta) fast path: peel the previous
	// candidate's appends (restoring tree AND index to the last loaded
	// layout), then reload trusting the index below that cursor. Every doubt
	// falls back to the full rebuild inside ReloadForBuild.
	//
	// Serialization: only the miner worker's single build goroutine calls
	// this, so the computer needs no lock of its own. The lock exists for
	// PrewarmMinerRootComputer (startup only); S11 times a wait on it here
	// in case a slow pre-warm after a restart is what a future stall waits on.
	if buildStallDiagEnabled {
		t0 := time.Now()
		bc.minerRCMu.Lock()
		bc.buildStallRootLockWaitNs.Add(int64(time.Since(t0)))
	} else {
		bc.minerRCMu.Lock()
	}
	defer bc.minerRCMu.Unlock()
	rc := bc.minerRC
	if rc == nil {
		rc = commitment.NewQMDBRootComputer()
		rc.EnableUndoRecording() // the candidate peel below needs per-build undo
		bc.minerRC = rc
	}
	rc.SetCold(tx)
	// Own-block fast path (N42_MINER_ADOPT_APPENDS=1): the block this tree
	// built is the parent of the block about to be built -- its root is the
	// parent header's root, the live tree's cursor is where this tree's
	// appends end, and no branch switch is queued -- so the tree already IS
	// the parent's post-state. Keep it instead of peeling and re-reading
	// the same entries from disk (persistWait + reload, ~450 ms a block on
	// the leader at 163k). Anything else falls through to the reload.
	if minerAdoptAppends && len(bc.minerPendingUndo) == 0 && rc.HasUnwrittenBuild() &&
		parentRoot != (types.Hash{}) && rc.Root() == parentRoot && bc.qmdbRootComputer != nil {
		// The tree IS the parent's post-state. Whatever the live tree has
		// written of it is adopted (dropped from the undo bookkeeping); what
		// it has not written yet -- our own block still in flight, two-deep
		// speculation, track 3c -- stays as pending appends and the next
		// build chains on top. Either way: no peel, no reload.
		liveNext, liveFlushed := bc.qmdbRootComputer.NextSlot(), bc.qmdbRootComputer.FlushedThrough()
		adopted := rc.AdoptOwnAppends(liveNext, liveFlushed)
		chained := rc.ChainPendingBuild()
		log.Debug("miner speculative tree continues on its own build", "root", parentRoot.Hex()[:12],
			"adopted", adopted, "chained", chained, "pending", rc.PendingBuilds(), "slot", rc.NextSlot(), "liveSlot", liveNext)
		return rc, nil
	}
	if err := rc.PeelAll(); err != nil {
		log.Debug("miner speculative tree peel failed; full reload", "err", err)
		rc.VoidIndexTrust()
	}
	if undo := rc.TakeUndo(); undo != nil {
		// Previous build's candidate ops are still on the speculative tree;
		// peel them so the index matches the last loaded layout again. A
		// failed peel just voids the trust — ReloadForBuild rebuilds.
		if err := rc.ApplyUndo(undo); err != nil {
			log.Debug("miner speculative tree candidate peel failed; full reload", "err", err)
			rc.VoidIndexTrust()
		}
	}
	bc.applyMinerRewindsLocked(rc, tx)
	if err := rc.ReloadForBuild(tx); err != nil {
		log.Warn("miner QMDB speculative reload failed; build abandoned", "err", err)
		return nil, err
	}
	// The reload lands on the APPLIED head. A chained build whose parent is
	// an own unwritten block reaches here only when the tree lost that
	// block's pending build -- a branch-switch rewind peeled it (round
	// 35zze: the leader unwound its own 13750006 to rebuild the height,
	// re-proposed the first block, and the next build chained on it over a
	// tree reloaded at 13750005; the reads were right through the snapshot,
	// the root was computed from the wrong base, six followers rejected it).
	// Refuse rather than seal a root nobody can reproduce; the production
	// trigger aligns the applied head to the parent and builds again.
	if parentRoot != (types.Hash{}) {
		if got := rc.Root(); got != parentRoot {
			return nil, fmt.Errorf("miner tree reloaded at root %x, build parent root %x: parent is not the applied head and its build is not on the tree",
				got[:6], parentRoot[:6])
		}
	}
	return rc, nil
}

// queueMinerRewind records a branch-switch undo for the miner's speculative
// tree. Called from the live unwind (under bc.lock); consumed by the next
// build under minerRCMu.
func (bc *BlockChain) queueMinerRewind(undo *qmdb.BlockUndo) {
	if undo == nil {
		return
	}
	bc.minerRCMu.Lock()
	defer bc.minerRCMu.Unlock()
	if bc.minerRC == nil {
		return // no speculative tree yet: its first load sees the store as it is
	}
	bc.minerPendingUndo = append(bc.minerPendingUndo, undo)
}

// applyMinerRewindsLocked peels queued branch-switch undos off the miner's
// speculative tree, newest-applied first as the live tree did. Caller holds
// minerRCMu and has already peeled the dangling candidate.
func (bc *BlockChain) applyMinerRewindsLocked(rc *commitment.QMDBRootComputer, tx kv.Tx) {
	pending := bc.minerPendingUndo
	bc.minerPendingUndo = nil
	for _, u := range pending {
		if err := rc.RewindForUndo(tx, u); err != nil {
			log.Warn("miner speculative tree: branch-switch rewind failed; next reload rebuilds from the entry log",
				"firstSlot", u.PrevNextSlot, "err", err)
			return // trust is void; the remaining records are moot
		}
	}
	if len(pending) > 0 {
		log.Info("miner speculative tree rewound for a branch switch", "records", len(pending))
	}
}

// PrewarmMinerRootComputer performs the startup pre-warm reload AND the
// cold-reader detach inside ONE minerRCMu critical section. The old
// pattern (NewMinerRootComputer, then SetCold(nil) outside the lock)
// could interleave with a real build acquiring the lock in between: the
// stale detach then nulled the cold reader the build was actively
// faulting through, panicking the miner group.
func (bc *BlockChain) PrewarmMinerRootComputer(tx kv.Tx) bool {
	if !bc.qmdbEnabled {
		return false
	}
	bc.minerRCMu.Lock()
	defer bc.minerRCMu.Unlock()
	if bc.minerRC != nil {
		// A real build already created (and is possibly actively using)
		// the shared computer OUTSIDE this lock — reloading or detaching
		// it here would peel the in-flight build's appends and null the
		// reader it is faulting through. The warm-up goal is met anyway.
		return false
	}
	rc := commitment.NewQMDBRootComputer()
	rc.EnableUndoRecording()
	bc.minerRC = rc
	rc.SetCold(tx)
	if undo := rc.TakeUndo(); undo != nil {
		if err := rc.ApplyUndo(undo); err != nil {
			log.Debug("miner speculative tree candidate peel failed; full reload", "err", err)
			rc.VoidIndexTrust()
		}
	}
	ok := true
	if err := rc.ReloadForBuild(tx); err != nil {
		log.Warn("miner QMDB speculative pre-warm reload failed", "err", err)
		ok = false
	}
	rc.SetCold(nil) // detach before the pre-warm tx dies — under the lock
	return ok
}
