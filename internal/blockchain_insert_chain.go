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
// Chain insertion entry points and insertChainMode.

package internal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/zkprover"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// InsertHeader is not implemented (light client mode unsupported).
func (bc *BlockChain) InsertHeader(headers []block.IHeader) (int, error) {
	return 0, errors.New("InsertHeader not implemented: light client mode is not yet supported")
}

// InsertBlock is deprecated; use InsertChain.
func (bc *BlockChain) InsertBlock(blocks []block.IBlock, isSync bool) (int, error) {
	return 0, fmt.Errorf("deprecated: use InsertChain instead, got %d blocks", len(blocks))
}

func (bc *BlockChain) skipBlock(err error) bool {
	return errors.Is(err, ErrKnownBlock)
}

// =============================================================================
// Chain Insertion
// =============================================================================

// InsertChain validates contiguity and inserts blocks into the canonical chain.
func (bc *BlockChain) InsertChain(chain []block.IBlock) (int, error) {
	if len(chain) == 0 {
		return 0, nil
	}
	for i := 1; i < len(chain); i++ {
		blk, prev := chain[i], chain[i-1]
		if blk.Number64().Cmp(uint256.NewInt(0).Add(prev.Number64(), uint256.NewInt(1))) != 0 || blk.ParentHash() != prev.Hash() {
			log.Error("Non contiguous block insert",
				"number", blk.Number64().String(), "hash", blk.Hash(),
				"parent", blk.ParentHash(),
				"prev number", prev.Number64(), "prev hash", prev.Hash(),
			)
			return 0, fmt.Errorf("non contiguous insert: item %d is #%s [%x..], item %d is #%s [%x..] (parent [%x..])", i-1, prev.Number64().String(),
				prev.Hash().Bytes()[:4], i, blk.Number64().String(), blk.Hash().Bytes()[:4], blk.ParentHash().Bytes()[:4])
		}
	}
	if err := bc.resolveChainFrames(chain); err != nil {
		return 0, err
	}
	bc.lock.Lock()
	defer bc.lock.Unlock()
	return bc.insertChain(chain, false)
}

// insertChainLag is the lag executor's import of one block: its write may
// be handed to the ordered writer (lag_writer.go), reported in handed.
func (bc *BlockChain) insertChainLag(b block.IBlock, authorizedSwitch bool) (handed bool, err error) {
	chain := []block.IBlock{b}
	if err := bc.resolveChainFrames(chain); err != nil {
		return false, err
	}
	bc.lock.Lock()
	defer bc.lock.Unlock()
	lag := &lagInsert{}
	_, err = bc.insertChainMode(chain, authorizedSwitch, lag)
	return lag.handed, err
}

// resolveChainFrames resolves every frame body in chain before the import
// takes bc.lock (a pull can wait on peers). It stops at the first block that
// cannot be resolved; the caller imports nothing.
func (bc *BlockChain) resolveChainFrames(chain []block.IBlock) error {
	for _, blk := range chain {
		if err := bc.ResolveFrames(blk); err != nil {
			return err
		}
	}
	return nil
}

// InsertChainAuthorized imports blocks WITH branch-switch authority: if a
// block's parent is a sibling of the applied head, the applied branch is
// reverted onto the incoming branch. Reserved for consensus-driven imports
// (the proposal chain-align in fetch-on-miss); passive paths (gossip, push,
// future-queue) must use InsertChain, which refuses to revert.
func (bc *BlockChain) InsertChainAuthorized(chain []block.IBlock) (int, error) {
	if len(chain) == 0 {
		return 0, nil
	}
	if err := bc.resolveChainFrames(chain); err != nil {
		return 0, err
	}
	// S11: same bc.lock, same diagnostic as AlignAppliedBranch above. A build
	// stall's InsertChainAuthorized call is the "import consensus parent"
	// fallback below AlignAppliedBranch in worker.go's commitWork, so this
	// wait lands in the same accumulator (TakeBuildStallLockWait), read right
	// after each call from the single build goroutine -- routine gossip/sync
	// imports calling this too could in principle add noise here, but that
	// path is reserved for consensus-driven imports, not routine traffic.
	if buildStallDiagEnabled {
		t0 := time.Now()
		bc.lock.Lock()
		bc.buildStallLockWaitNs.Add(int64(time.Since(t0)))
	} else {
		bc.lock.Lock()
	}
	defer bc.lock.Unlock()
	return bc.insertChain(chain, true)
}

func (bc *BlockChain) insertChain(chain []block.IBlock, authorizedSwitch bool) (int, error) {
	return bc.insertChainMode(chain, authorizedSwitch, nil)
}

// insertChainMode is insertChain; lag is non-nil for the lag executor's
// import, whose write may go to the ordered writer.
func (bc *BlockChain) insertChainMode(chain []block.IBlock, authorizedSwitch bool, lag *lagInsert) (int, error) {
	if bc.insertStopped() {
		return 0, nil
	}
	if bc.E1Light() {
		return bc.e1LightQueue(chain), nil
	}
	// Only the child of the block whose write is in flight imports while
	// that write runs; everything else sees the durable state.
	if bc.lagWrite.inflight != nil && !bc.lagWriteFastCandidate(chain, lag) {
		if err := bc.joinLagWrite(); err != nil {
			return 0, err
		}
	}

	var (
		stats     = insertStats{startTime: time.Now()}
		lastCanon block.IBlock
	)
	defer func() {
		_ = lastCanon
	}()

	// Parallel header verification
	headers := make([]block.IHeader, len(chain))
	seals := make([]bool, len(chain))
	for i, blk := range chain {
		headers[i] = blk.Header()
		seals[i] = true
	}
	abort, results := bc.engine.VerifyHeaders(bc, headers, seals)
	defer close(abort)

	it := newInsertIterator(chain, results, bc.validator)
	blk, err := it.next()

	// Skip already-known blocks
	if bc.skipBlock(err) {
		// Leader-driven chains: an already-known block never moves the canonical
		// chain at import time — canonicalization is commit-driven
		// (CommitToCanonical is the single authority). writeKnownBlock would
		// re-enter the reorg/head-write path for a non-canonical sibling and
		// fight the commit authority over the canonical table.
		if bc.canonicalByCommitOnly() {
			for blk != nil && bc.skipBlock(err) {
				// A block that is STORED (even canonical) is not necessarily
				// APPLIED: the QMDB world state may sit on a same-height
				// sibling (the node's own dead candidate). On an AUTHORIZED
				// import (consensus/catch-up named this branch) such a block
				// must NOT be skipped — nothing else will ever move the
				// applied state off the sibling and onto it (observed live: a
				// node held the committed block in its DB, canonical even
				// pointed at it, but the applied head was its own candidate;
				// every fetched range was "already known" at that height and
				// the child failed unknown-ancestor forever). The main loop
				// accepts ErrKnownBlock and performs the unwind + re-exec.
				if authorizedSwitch && bc.knownBlockNeedsAuthorizedReplay(blk) {
					break
				}
				log.Debug("Ignoring already known block", "number", blk.Number64(), "hash", blk.Hash())
				stats.ignored++
				blk, err = it.next()
			}
		} else {
			var (
				reorg         bool
				current       = bc.CurrentBlock()
				currentNumber *uint256.Int
			)
			currentNumber, err = requireBlockNumber(current, "current block number unavailable")
			if err != nil {
				return it.index, err
			}
			for blk != nil && bc.skipBlock(err) {
				reorg, err = bc.forker.ReorgNeeded(current.Header(), blk.Header())
				if err != nil {
					return it.index, err
				}
				if reorg {
					blockNumber, numberErr := requireBlockNumber(blk, "block number unavailable")
					if numberErr != nil {
						return it.index, numberErr
					}
					if blockNumber.Uint64() > currentNumber.Uint64() || bc.GetCanonicalHash(blockNumber) != blk.Hash() {
						break
					}
				}
				log.Debug("Ignoring already known block", "number", blk.Number64(), "hash", blk.Hash())
				stats.ignored++
				blk, err = it.next()
			}
			for blk != nil && bc.skipBlock(err) {
				log.Debug("Writing previously known block", "number", blk.Number64(), "hash", blk.Hash())
				if err := bc.writeKnownBlock(nil, blk); err != nil {
					return it.index, err
				}
				lastCanon = blk
				blk, err = it.next()
			}
		}
	}

	switch {
	case errors.Is(err, ErrPrunedAncestor):
		// Authorized branch switch: the parent's STATE is missing because the
		// applied head is a LOSING same-height sibling of this block (the
		// world state sits on the sibling, so the parent has a header but no
		// state → ErrPrunedAncestor). insertSideChain cannot resolve this —
		// it walks down looking for a stateful ancestor it will never find
		// (QMDB keeps state only at the applied head) and the whole range is
		// rejected forever (observed live: a laggard spinning on
		// pruned-ancestor for every fetched range). The remedy is the same
		// authorized unwind the main loop performs: revert the applied
		// sibling branch so the parent's state is restored, then process this
		// block on the normal path.
		if authorizedSwitch {
			if bn, nerr := requireBlockNumber(blk, "block number unavailable"); nerr == nil {
				if uerr := bc.unwindForReimport(bn.Uint64(), blk.ParentHash(), true); uerr == nil {
					log.Info("authorized sibling switch: applied branch unwound for pruned-ancestor import",
						"number", bn.Uint64(), "hash", blk.Hash().Hex()[:12])
					err = nil // parent state restored — fall through to normal processing
				} else {
					log.Warn("authorized sibling switch failed; falling back to sidechain path",
						"number", bn.Uint64(), "err", uerr)
				}
			}
		}
		if err != nil {
			log.Debug("Pruned ancestor, inserting as sidechain", "number", blk.Number64(), "hash", blk.Hash())
			return bc.insertSideChain(blk, it, authorizedSwitch)
		}

	case errors.Is(err, ErrFutureBlock) || isUnknownAncestorErr(err):
		// Queue any block whose parent we don't have yet (not only when the parent
		// is already queued). Direct-pushed HotStuff blocks can arrive out of order
		// (e.g. block N+1 before N); future-queue them so they import once the
		// parent arrives, instead of rejecting them as bad blocks.
		for blk != nil && (it.index == 0 || isUnknownAncestorErr(err)) {
			log.Debug("Future block, postponing import", "number", blk.Number64(), "hash", blk.Hash())
			if err := bc.AddFutureBlock(blk); err != nil {
				return it.index, err
			}
			blk, err = it.next()
		}
		stats.queued += it.processed()
		stats.ignored += it.remaining()
		return it.index, err

	case err != nil && !errors.Is(err, ErrKnownBlock):
		bc.futureBlocks.Remove(blk.Hash())
		stats.ignored += len(it.chain)
		bc.reportBlock(blk, nil, err)
		return it.index, err
	}

	// Process blocks
	// Ahead executions (lag pipeline) taken by this call: their read
	// transactions stay open until the blocks are written.
	var aheadTaken []*aheadExec
	defer func() {
		for _, a := range aheadTaken {
			a.release()
		}
	}()

	evmRecord := func(ctx context.Context, db kv.RwDB, blk block.IBlock, ahead *aheadExec, f func(tx kv.Tx, ibs *state.IntraBlockState, reader state.StateReader, writer state.WriterWithChangeSets) (map[types.Address]*uint256.Int, error)) (*state.IntraBlockState, map[types.Address]*uint256.Int, error) {
		tx, err := db.BeginRo(ctx)
		if err != nil {
			return nil, nil, err
		}
		defer tx.Rollback()

		var ibs *state.IntraBlockState
		var stateReader state.StateReader
		if ahead != nil {
			// Executed ahead on the parent's snapshot (lag_pipeline_exec.go):
			// its state and readers carry over; the root and the write run
			// here, on the live tree, as for any block.
			ibs, stateReader = ahead.ibs, ahead.reader
		} else {
			plainReader := state.NewPlainStateReader(tx)
			stateReader = plainReader
			// The optional batch reader is limited to a single MDBX database and the
			// serial plain-state path; wrapper caches and the experimental shared-reader
			// parallel executor retain their existing behavior.
			if _, directMDBX := db.(*mdbx.MdbxKV); directMDBX && accountPrefetchWorkers > 0 &&
				!bc.parallelEVM && !bc.prefetchEnabled && commitment.QMDBStateReadMode() == commitment.QMDBReadOff {
				stateReader = &blockAccountPrefetchReader{PlainStateReader: plainReader, ctx: ctx, db: db, viewID: tx.ViewID(), workers: accountPrefetchWorkers}
			}
			if cache := layered.ExtractCache(db); cache != nil {
				stateReader = state.NewCachedStateReader(stateReader, cache)
			}
			// N42_STATE_READ_QMDB (default off) reads accounts and storage slots
			// from the QMDB tree instead of the address-keyed `Account` table.
			// Round 19's write probe put `Account` at 16,096 random-keyed rows a
			// block -- ~64.4 MB of the 71.0 MB MDBX dirties, 90% of the bytes from
			// 10% of the payload -- and the tree already holds the same values in
			// the same encoding. `verify` reads both and reports divergence while
			// still ANSWERING from the plain reader, so an equivalence round cannot
			// change a block's outcome; nothing here stops the plain write.
			if mode := commitment.QMDBStateReadMode(); mode != commitment.QMDBReadOff &&
				bc.qmdbEnabled && bc.qmdbRootComputer != nil {
				if src := bc.qmdbRootComputer.ValueSource(); src != nil {
					stateReader = commitment.NewQMDBStateReader(src, stateReader, mode)
				}
			}
			// The parallel processor seeds this layer with the block's
			// delta-credited recipients, read across its workers, so the
			// block-end fold does not read them from the store one by one.
			prefetch := state.NewAccountPrefetch(stateReader)
			stateReader = prefetch
			ibs = state.New(stateReader)
			ibs.SetAccountPrefetch(prefetch)
			// The root takes the block's prefetched credits without
			// materialising them; CommitBlock does, in the write.
			ibs.SetDetachedPendingCredits(bc.qmdbEnabled)
		}
		bc.lagPipeHook(blk, ibs)
		// Inject root computer for tree-based state root computation.
		// JMT: only for fresh chains where all blocks use JMT from genesis.
		// MPT: always inject when enabled (branches persisted in MDBX).
		if bc.rootComputer != nil && bc.jmtForBlockProcessing {
			ibs.SetRootComputer(bc.rootComputer)
		}
		if bc.mptEnabled && bc.mptRootComputer != nil {
			bc.mptRootComputer.SetReadTx(tx)
			bc.mptRootComputer.SetStateReader(commitment.NewPlainStateMPTReader(tx))
			ibs.SetRootComputer(bc.mptRootComputer)
		}
		if bc.qmdbEnabled && bc.qmdbRootComputer != nil {
			// QMDB faults flushed entries back from the committed positional log
			// via this block's read tx; the in-RAM index keeps key->slot resident.
			bc.qmdbRootComputer.SetCold(tx)
			ibs.SetRootComputer(bc.qmdbRootComputer)
		}
		stateWriter := state.NewNoopWriter()

		nopay, err := f(tx, ibs, stateReader, stateWriter)
		if err != nil {
			return nil, nil, err
		}
		return ibs, nopay, nil
	}

	for ; blk != nil && (err == nil || errors.Is(err, ErrKnownBlock)); blk, err = it.next() {
		if bc.insertStopped() {
			log.Debug("Abort during block processing")
			break
		}

		// Import-path phase accounting (observability only). dHdr and dBody were
		// already paid inside the it.next() that produced this block, so they are
		// captured here and added back into the total below.
		tIter := time.Now()
		dHdr, dBody := it.lastVerifyWait, it.lastBodyCheck
		var dAlign, dProcess, dValidate, dWrite time.Duration
		var procPhases ProcessPhases

		// S39 (docs/QS_BLOCK_TIME_BUDGET.md 6dr/6ds): tIter above IS the start
		// of InsertChain's own per-block processing -- 6dr's own
		// "MISSING-STAMP", distinct from the block-push handler's
		// socket-receipt "arrived" stamp and from whatever lock/queue wait
		// sits between the handler's own SetQueueTMs (just before this call)
		// and here.
		if contentionDiagEnabled {
			if b, ok := blk.(interface{ SetInsertStartTMs(int64) }); ok {
				b.SetInsertStartTMs(tIter.UnixMilli())
			}
		}

		log.Tracef("Current block: number=%v, hash=%v, difficult=%v | Insert block block: number=%v, hash=%v, difficult= %v",
			bc.CurrentBlock().Number64(), bc.CurrentBlock().Hash(), bc.CurrentBlock().Difficulty(), blk.Number64(), blk.Hash(), blk.Difficulty())

		// Out-of-order direct-pushed block: if the parent isn't imported yet,
		// future-queue this block instead of executing it. EVM execution would fail
		// to read the missing parent state and report a bad block; queuing lets it
		// import once the parent arrives and processFutureBlocks retries it. This is
		// the norm for HotStuff blocks pushed directly from the leader, which can
		// arrive before their parent.
		if n := blk.Number64().Uint64(); n > 0 && !bc.HasBlock(blk.ParentHash(), n-1) {
			if aerr := bc.AddFutureBlock(blk); aerr != nil {
				return it.index, aerr
			}
			stats.queued++
			continue
		}

		// ZK fast path: if the block has a valid ZK proof and ZK verification
		// is enabled, skip EVM re-execution and trust the proven state root.
		hasZKProof := bc.zkProving && blk.Body() != nil && len(blk.Body().ZKProof()) > 0
		if hasZKProof {
			if bc.tryZKFastPath(blk) {
				start := time.Now()
				wstart := time.Now()
				status, werr := bc.writeBlockWithState(blk, nil, nil, nil)
				if werr != nil {
					return it.index, werr
				}
				blockWriteTimer.Observe(float64(time.Since(wstart)))
				blockInsertTimer.Observe(float64(time.Since(start)))
				stats.processed++
				if status == CanonStatTy {
					lastCanon = blk
				}
				log.Info("Block accepted via ZK proof (EVM skipped)", "number", blk.Number64(), "hash", blk.Hash())
				continue
			}
			// Proof exists but verification failed.
			if bc.zkRequireProof {
				log.Warn("Block rejected: ZK proof verification failed", "number", blk.Number64(), "hash", blk.Hash())
				return it.index, errZKProofRequired
			}
			// Not required — fall through to EVM execution.
		} else if bc.zkRequireProof {
			// No proof present but required.
			log.Warn("Block rejected: ZK proof required but not provided", "number", blk.Number64(), "hash", blk.Hash())
			return it.index, errZKProofRequired
		}

		start := time.Now()
		var receipts block.Receipts
		var logs []*block.Log
		var usedGas uint64
		concreteBlock, err := requireConcreteBlock(blk, "unexpected block type")
		if err != nil {
			return it.index, err
		}
		concreteHeader, err := requireConcreteHeader(concreteBlock.Header(), "unexpected block header type")
		if err != nil {
			return it.index, err
		}
		blockNumber, err := requireBlockNumber(blk, "block number unavailable")
		if err != nil {
			return it.index, err
		}

		// Applied-evidence known-block gate. The validator's ErrKnownBlock uses
		// HasBlockAndState = IsCanonicalHash, which on a commit-authority-only
		// chain is FALSE for a block that was just written but not yet
		// committed — so a concurrent re-import of the very block this node
		// just applied (fetch-on-miss racing the leader's own write; observed
		// live) sailed past every known-block skip, and unwindForReimport
		// "branch-switched" the block onto itself: a pointless revert+re-execute
		// of an identical block that destabilized the live tree under the
		// next build. A block whose state IS the applied lineage needs nothing
		// from this loop.
		if bc.qmdbEnabled && bc.HasAppliedBlock(blk.Hash(), blockNumber.Uint64()) {
			log.Debug("Ignoring already applied block", "number", blockNumber.Uint64(), "hash", blk.Hash())
			stats.ignored++
			continue
		}

		// HotStuff branch switch: if the applied world state is not the state of
		// this block's parent (same-height sibling re-import / view-change
		// re-proposal), cleanly revert applied blocks until it is — the EVM must
		// read the parent's state and the QMDB tree must re-append at identical
		// slots (its root is append-order-dependent). Committed BEFORE evmRecord
		// so the execution's read snapshot sees the reverted state. No-op on the
		// plain forward path.
		tAlign := time.Now()
		// The child of the block whose write is in flight: the tree is exactly
		// its parent (checked), so the marker-based alignment below, which
		// reads the durable marker one block behind, has nothing to do.
		lagFast := bc.lagWrite.inflight != nil && bc.lagWriteFastReady()
		if lagFast {
			bc.lagWrite.overlap.Add(1)
		} else {
			if err := bc.joinLagWrite(); err != nil {
				return it.index, err
			}
		}
		if lagFast {
			// skip the alignment
		} else if uerr := bc.unwindForReimport(blockNumber.Uint64(), blk.ParentHash(), authorizedSwitch); uerr != nil {
			if errors.Is(uerr, consensus.ErrUnknownAncestor) || errors.Is(uerr, errRevertUnavailable) {
				// Either the incoming block's parent is a sibling this node never
				// applied, or the revert hit a recoverable local QMDB condition
				// (stale/missing leaf blob). Both are non-fatal: future-queue the
				// block and let it retry — never crash, never mark it BAD.
				if errors.Is(uerr, errRevertUnavailable) {
					log.Warn("branch-switch revert unavailable; future-queuing block",
						"number", blockNumber.Uint64(), "hash", fmt.Sprintf("%x", blk.Hash().Bytes()[:8]), "err", uerr)
				}
				if aerr := bc.AddFutureBlock(blk); aerr != nil {
					return it.index, aerr
				}
				stats.queued++
				continue
			}
			return it.index, uerr
		}

		// Second line of defense against a dangling candidate: a leader whose
		// sealed block lost the view (or never sealed) left that candidate's
		// appends on the live tree; executing the incoming winner on top would
		// compute a shifted root. The miner peels on its own path — this covers
		// the import path racing ahead of it.
		if !lagFast {
			bc.PeelDanglingQMDBAppends()
		}
		// Pre-execution invariant: the live tree must BE the parent's
		// post-state. Any tree/marker discontinuity (observed live: a store
		// rewound by an unwind while the marker claimed blocks the tree never
		// held) would otherwise execute on the wrong base and produce a root
		// the cluster rejects — silently before root verification existed,
		// loudly-but-wastefully after. Realign the marker to the tree and
		// future-queue the block; catch-up then replays forward from the
		// tree's true height.
		if lagFast {
			// checked by lagWriteFastReady
		} else if qerr := bc.ensureQMDBTreeAtParent(blk); qerr != nil {
			if errors.Is(qerr, errQMDBBehindParent) {
				log.Debug("block ahead of the applied state; future-queuing",
					"number", blockNumber.Uint64())
			} else {
				log.Warn("QMDB tree/marker discontinuity before execution; realigned and queueing",
					"number", blockNumber.Uint64(), "err", qerr)
			}
			if aerr := bc.AddFutureBlock(blk); aerr != nil {
				return it.index, aerr
			}
			stats.queued++
			continue
		}
		dAlign = time.Since(tAlign)
		// Lag pipeline: this block may have executed ahead, on its parent's
		// snapshot, while the parent was rooted and written.
		tAhead := time.Now()
		ahead := bc.lagPipeTake(blk, blockNumber.Uint64())
		dAheadWait := time.Since(tAhead)
		if lagFast && ahead == nil {
			// The serial path reads the store, which does not hold the
			// parent yet: let its write land first.
			if err := bc.joinLagWrite(); err != nil {
				return it.index, err
			}
		}
		if ahead != nil {
			aheadTaken = append(aheadTaken, ahead)
		}
		// Under deferred execution the write stores the block's executed
		// result; its receipts part needs only the receipts, final once the
		// ahead run collected them, so it is hashed while the state root runs.
		deferredExec := bc.chainConfig != nil && bc.chainConfig.IsDeferredExecution(concreteHeader.Time)
		bc.receiptsFuture = nil
		if ahead != nil && deferredExec && ahead.run != nil {
			bc.receiptsFuture = startReceiptsResult(bc.chainConfig, blockNumber.Uint64(), blk.Hash(), ahead.run.Receipts)
		}
		// N42_LAG_WRITE_EARLY_PREP: the write's preparation may start inside
		// the root (lag_writer.go lagWriteEarlyPrep); earlySlot then holds it.
		var earlySlot *earlyPrepSlot
		earlyPrep := func() *stateWritePrep {
			if earlySlot == nil {
				return nil
			}
			return earlySlot.settle()
		}
		ibs, nopay, err := evmRecord(bc.ctx, bc.ChainDB, blk, ahead, func(tx kv.Tx, ibs *state.IntraBlockState, reader state.StateReader, writer state.WriterWithChangeSets) (map[types.Address]*uint256.Int, error) {
			earlySlot = bc.lagWriteEarlyPrepArm(concreteBlock, ibs, lag, ahead)
			getHeader := func(hash types.Hash, number uint64) *block.Header {
				return bc.readHeaderOrPending(tx, hash, number)
			}
			blockHashFunc := GetHashFn(concreteHeader, getHeader)

			// Start state prefetching in background if cache is available.
			if bc.prefetchEnabled && ahead == nil {
				prefetcher := NewStatePrefetcher(bc.chainConfig)
				if bc.prefetchPredictor != nil {
					prefetcher.SetPredictor(bc.prefetchPredictor)
				}
				prefetcher.Prefetch(concreteBlock, reader)
				defer prefetcher.Close()
			}

			if bc.chainConfig != nil && bc.chainConfig.IsDeferredExecution(concreteBlock.Header().(*block.Header).Time) {
				hdr := concreteBlock.Header().(*block.Header)
				parentHdr := bc.readHeaderOrPending(tx, hdr.ParentHash, hdr.Number.Uint64()-1)
				if parentHdr == nil {
					return nil, fmt.Errorf("%w: parent header %x of block %d not stored", ErrDeferredResultUnknown, hdr.ParentHash[:8], hdr.Number.Uint64())
				}
				if err := checkDeferredHeader(bc.chainConfig, tx, hdr, parentHdr); err != nil {
					if errors.Is(err, ErrDeferredResultUnknown) {
						// The parent is stored but not applied here yet: queue
						// and retry after it lands, like a missing ancestor.
						return nil, fmt.Errorf("%w: %w", ErrPrunedAncestor, err)
					}
					bc.reportBlock(blk, nil, err)
					return nil, fmt.Errorf("%w: %w", consensus.ErrExecutionInvalid, err)
				}
			}
			pstart := time.Now()
			var nopay map[types.Address]*uint256.Int
			if ahead != nil {
				receipts, nopay, logs, usedGas, err = bc.process.(*StateProcessor).FinishParallel(concreteBlock, ibs, ahead.run)
			} else if bc.parallelEVM {
				if sp, ok := bc.process.(*StateProcessor); ok {
					receipts, nopay, logs, usedGas, err = sp.ProcessParallel(concreteBlock, ibs, reader, writer, blockHashFunc)
				} else {
					receipts, nopay, logs, usedGas, err = bc.process.Process(concreteBlock, ibs, reader, writer, blockHashFunc)
				}
			} else {
				receipts, nopay, logs, usedGas, err = bc.process.Process(concreteBlock, ibs, reader, writer, blockHashFunc)
			}
			if err != nil {
				// Only a deterministic execution / consensus violation is a bad
				// block. Transient/internal failures — a nonce-too-high ordering
				// gap (block ran ahead of the executed state), context
				// cancellation, an unavailable ancestor, or a DB/IO error wrapped
				// as consensus.ErrInternal — are RETRYABLE: the outer loop queues
				// or retries them. Marking those BAD cached a healthy block as bad
				// until a restart cleared the LRU (the recurring QMDB pain); the
				// is_validation_error split (mirroring reth) prevents it.
				if consensus.IsInternalError(err) || strings.Contains(err.Error(), "nonce too high") {
					return nil, err
				}
				bc.reportBlock(blk, receipts, err)
				return nil, fmt.Errorf("%w: %w", consensus.ErrExecutionInvalid, err)
			}
			ptime := time.Since(pstart)
			dProcess = ptime
			// Split Process into EVM execution vs state root #3. Both the serial
			// Process and the strict parallel run publish their breakdown (the
			// parallel one adds setup and collect).
			if sp, ok := bc.process.(*StateProcessor); ok {
				procPhases = sp.LastPhases()
			}

			vstart := time.Now()
			if err := bc.validator.ValidateState(blk, ibs, receipts, usedGas); err != nil {
				bc.reportBlock(blk, receipts, err)
				return nil, fmt.Errorf("%w: %w", consensus.ErrExecutionInvalid, err)
			}
			vtime := time.Since(vstart)
			dValidate = vtime

			blockExecutionTimer.Observe(float64(ptime))
			blockValidationTimer.Observe(float64(vtime))
			return nopay, nil
		})
		if err != nil {
			if ep := earlyPrep(); ep != nil {
				// Nothing writes this block; the preparation reads through
				// the ahead run's transaction, released when this call ends.
				_, _ = ep.wait()
			}
			// The execution tx above rolled back, but the live QMDB tree is
			// NOT transactional: if execution got as far as ComputeRoot (a
			// ValidateState root mismatch always does), the failed block's
			// appends are already on the tree, undo row never persisted —
			// they could never be unwound and every later root would be
			// silently shifted (observed live as disk/tree/cluster
			// divergence). Peel them off with the in-memory undo record.
			bc.receiptsFuture = nil
			bc.revertUncommittedQMDBAppends(blockNumber.Uint64())
			bc.lagPipeSettle(blk.Hash(), false)
			if strings.Contains(err.Error(), "nonce too high") {
				// Execution ran ahead of the applied state (see evmRecord):
				// queue the block for replay once its prefix lands — the same
				// treatment as an unknown ancestor. Under transaction load
				// this fired about once a minute as a transient, self-healing
				// "BAD BLOCK" that was never actually bad.
				log.Debug("nonce gap during execution; future-queuing block",
					"number", blk.Number64().Uint64(), "err", err)
				if aerr := bc.AddFutureBlock(blk); aerr != nil {
					return it.index, aerr
				}
				stats.queued++
				continue
			}
			return it.index, err
		}

		// Early vote: execution and state validation just succeeded, which is
		// exactly what the import-gated vote certifies. Fire the consensus
		// notification NOW, on its own goroutine, so the vote round overlaps
		// the ~150-250ms persistence below instead of waiting behind it. A
		// writeBlockWithState failure after this is a local durability
		// problem (the node re-imports on recovery), not a vote-safety one.
		if bc.executedHook != nil {
			if hdr, ok := blk.Header().(*block.Header); ok && hdr != nil {
				h, th, ph, num, extra := blk.Hash(), blk.TxHash(), hdr.ParentHash, hdr.Number.Uint64(), hdr.Extra
				go bc.executedHook(h, th, ph, num, extra)
			}
		}

		if bc.receiptsFuture == nil && deferredExec {
			// Serial path: hashed while the write's earlier tables land.
			bc.receiptsFuture = startReceiptsResult(bc.chainConfig, blockNumber.Uint64(), blk.Hash(), receipts)
		}
		// N42_LAG_RESULT_EARLY: the result is final now; the leader's gate
		// and build may take it before the hand-off's wait for the previous
		// write (lag_gate_applied.go). Withdrawn once the hand-off returned.
		var earlyRes *lagEarlyResult
		if lag != nil {
			earlyRes = bc.lagPublishEarlyResult(concreteBlock, receipts, ibs)
		}
		wstart := time.Now()
		var status WriteStatus
		if err = lagPipeInjectedFailure(blk.Hash()); err == nil {
			var ep *stateWritePrep
			if earlySlot != nil {
				// N42_LAG_WRITE_EARLY_PREP_DELAY: an armed preparation the
				// child's launch has not started yet starts now.
				ep = earlySlot.forHandoff()
			}
			if lag != nil && (ep != nil || bc.lagWriteUsable(blk, ibs)) {
				// The ordered writer makes it durable; the next block's root
				// overlaps the write (lag_writer.go).
				var handAhead *aheadExec
				if ahead != nil {
					// Its read transaction stays open until the write is done.
					handAhead = ahead
					aheadTaken = aheadTaken[:len(aheadTaken)-1]
				}
				if err = bc.lagWriteHandOff(concreteBlock, receipts, ibs, nopay, handAhead, ep); err == nil {
					lag.handed = true
					status = SideStatTy
				} else if handAhead != nil {
					aheadTaken = append(aheadTaken, handAhead)
				}
			} else {
				status, err = bc.writeBlockWithState(blk, receipts, ibs, nopay)
			}
		} else if ep := earlyPrep(); ep != nil {
			// Not written: let the preparation finish with the state it owns.
			_, _ = ep.wait()
		}
		bc.lagWithdrawEarlyResult(earlyRes)
		bc.receiptsFuture = nil
		if err != nil {
			// writeBlockWithState rolled its tx back, but execution above already
			// put this block's appends on the non-transactional live QMDB tree
			// with the undo still in RAM. Peel them now under bc.lock — the same
			// treatment the execution-failure path (above) and the leader-seal
			// path (WriteBlockWithState) already apply. Leaving them dangling for
			// "the next insertChain's PeelDanglingQMDBAppends" opens a race: a
			// leader WriteBlockWithState acquiring bc.lock first would ComputeRoot
			// over the dangling appends (its checkQMDBLeaderSealParent still sees
			// marker==parent because this tx rolled back), trip the reproduce
			// guard, and StartUndoRecording would overwrite the dangling undo —
			// the appends then become un-peelable and the node wedges until a
			// restart reloads the tree from disk. No-op for non-QMDB / nothing
			// appended.
			bc.revertUncommittedQMDBAppends(blockNumber.Uint64())
			bc.lagPipeSettle(blk.Hash(), false)
			return it.index, err
		}
		bc.lagPipeSettle(blk.Hash(), true)
		dWrite = time.Since(wstart)
		blockWriteTimer.Observe(float64(dWrite))
		blockInsertTimer.Observe(float64(time.Since(start)))

		// One compact, greppable line per imported block — the follower-side
		// counterpart of "miner: build phases" (which covers only the leader's
		// build) and of "blockwrite phases" (which covers only what happens once
		// execution is done). Between them these three lines account for a block
		// end to end on both roles.
		//
		//	hdr   wait on the parallel VerifyHeaders result (HotStuff BLS seal)
		//	body  ValidateBody — recomputes the transaction root over every tx
		//	align QMDB tree/marker alignment: unwindForReimport + dangling-append
		//	      peel + ensureQMDBTreeAtParent. Zero on the plain forward path.
		//	recov parallel sender recovery. Work that used to be hidden inside
		//	      exec, one serial secp256k1 recovery per transaction; the miner
		//	      never pays it because the pool cached the sender at admission.
		//	exec  pure EVM: the per-transaction ApplyTransaction loop. This is the
		//	      like-for-like counterpart of the miner's fillTx.
		//	root  state root #3 (engine.Finalize -> ibs.IntermediateRoot on the
		//	      LIVE commitment tree) — the counterpart of the miner's finalize,
		//	      NOT of fillTx. proc is recov+prep+exec+root, i.e. all of Process.
		//	valid ValidateState: receipt bloom + receipt root.
		//	write writeBlockWithState in full; "blockwrite phases" breaks it down.
		if fn := importPhasesHook; fn != nil {
			var at *AheadTimes
			if ahead != nil {
				at = &AheadTimes{Pre: ahead.pre, Run: ahead.took, Wait: dAheadWait}
			}
			fn(blockNumber.Uint64(), procPhases, dProcess, dValidate, dWrite, at)
		}
		{
			dTotal := dHdr + dBody + time.Since(tIter)
			fields := []interface{}{
				"n", blockNumber.Uint64(), "txs", len(blk.Transactions()), "gas", usedGas,
				"hdr", dHdr, "body", dBody, "align", dAlign,
				"recov", procPhases.Recover, "prefetch", procPhases.Prefetch, "prep", procPhases.Prep,
				"exec", procPhases.Exec, "root", procPhases.Finalize,
				"setup", procPhases.Setup, "collect", procPhases.Collect,
				"proc", dProcess, "valid", dValidate, "write", dWrite, "total", dTotal,
				"tMs", time.Now().UnixMilli(),
			}
			if ahead != nil {
				// Executed ahead: exec/collect above ran while the parent was
				// rooted and written; aheadWait is what this import waited
				// for it, aheadRun its whole run. launch*: the launch against
				// the parent's snapshot publication (lagPipeLaunch); c*/apply*:
				// collect's parts and the MVS fold's split.
				fields = append(fields, "ahead", true, "aheadWait", dAheadWait, "aheadRun", ahead.took, "aheadPre", ahead.pre,
					"queueWaitMs", ahead.queueWait.Milliseconds(),
					"aheadStartTMs", ahead.started.UnixMilli(), "stmEndTMs", tsMs(procPhases.StmEnd),
					"prepStartTMs", tsMs(ahead.prepStart), "prepEndTMs", tsMs(ahead.prepEnd),
					"launchBy", lagLaunchName(ahead.launchBy), "launchLag", ahead.launchLag,
					"childArriveAfterHook", ahead.queuedAfterHook, "childAvailCheck", ahead.availCheck,
					"takeAfterHook", aheadTakeAfterHook(ahead, tAhead),
					"setupAff", procPhases.SetupAffinity, "setupSys", procPhases.SetupBlockStart,
					"cRcpt", procPhases.CollectReceipts, "cApply", procPhases.CollectApply,
					"cPref", procPhases.CollectPrefetch, "cFees", procPhases.CollectFees,
					"applyWalk", procPhases.ApplyWalk, "applyAcct", procPhases.ApplyAccounts, "applySto", procPhases.ApplyStorage,
					"cRcptRun", procPhases.CollectRcptRun, "earlyPub", lagEarlyPublishOn, "finFast", state.FinalizeFastOn(), "foldNJ", state.FoldNoJournalOn())
				// aheadPre's split (r69a): drain, frames, the claim of the
				// preparation (fill: its base prefill part), bind the rest;
				// read per launchBy (N42_AHEAD_PRE_EARLY cuts a late fill).
				fields = append(fields, "aheadPreDrain", ahead.preDrain, "aheadPreFrames", ahead.preFrames,
					"aheadPreClaim", ahead.preClaim, "aheadPreFill", ahead.preFill,
					"aheadPreBind", ahead.pre-ahead.preDrain-ahead.preFrames-ahead.preClaim,
					"aheadPreCut", ahead.preCut, "preEarly", aheadPreEarlyOn, "resultEarly", lagResultEarlyOn)
			}
			// The root's own split (P7: the fleet logged only the total):
			// the wait for the tree's readers, the dirty-set collection, the
			// apply, the snapshot publish, and the credits the root took
			// detached (P4b) against those it materialised as objects.
			if cr := commitment.LastComputeRootPhases(); cr != nil {
				fields = append(fields, "rootLock", cr.LockWait, "rootOps", cr.Collect, "rootApply", cr.Apply)
			}
			// Finalize's span on an ahead run up to the post-state hook (the
			// child's launch), step by step, then the hook, the root
			// computer and what follows it (ProcessPhases.FinPreHook).
			if rp := procPhases.RootPrep; ahead != nil && !rp.Enter.IsZero() {
				fields = append(fields, "fPreHook", procPhases.FinPreHook, "fBlockEnd", procPhases.FinBlockEnd,
					"fSysCall", procPhases.FinSysCall, "fSysFin", procPhases.FinSysFinalize,
					"fEngine", procPhases.FinEngine, "fPending", rp.Pending, "fDetach", rp.Detach, "fMat", rp.Mat,
					"fMerge", rp.Merge, "fSort", rp.Sort, "fLoop", rp.Loop, "fTail", rp.Tail, "fDirty", rp.Dirty,
					"fHook", rp.Hook, "fCompute", rp.Compute, "fAfter", procPhases.FinAfter)
				if state.FinalizeHoistOn() {
					_, fb := state.FinalizeHoistStats()
					fields = append(fields, "fHoist", rp.Hoisted, "fHoistFb", fb)
					if rp.Hoisted {
						// fDetach's parts: the wait for the hoisted goroutine,
						// its run from the arm, and the arm-to-take gap.
						fields = append(fields, "fHoistWait", rp.HoistWait, "fHoistRun", rp.HoistRun, "fHoistGap", rp.HoistGap)
					}
				}
				// fLoop's object resolution; N42_PREHOOK_CUT's credit inserts
				// (their run from before fSort, and fLoop's wait for them).
				fields = append(fields, "fObjs", rp.Objs, "fCutMode", state.PrehookCutMode())
				if rp.Cut {
					fields = append(fields, "fCredIns", rp.CredIns, "fCredJoin", rp.CredJoin)
				}
			}
			if det, mat := state.LastRootCredits(); det+mat > 0 {
				fields = append(fields, "creditsDetached", det, "creditsMaterialised", mat)
			}
			fields = append(fields, "publish", time.Duration(lagPublishNanos.Load()))
			if lag != nil && lag.handed {
				// write above is the hand-off to the ordered writer;
				// handWait is its wait for the previous block's write.
				fields = append(fields, "handed", true, "handWait", time.Duration(bc.lagWrite.lastWait.Load()),
					"handoffUs", bc.lagWrite.lastHandoff.Load()/1000, "handLean", lagHandoffLean)
			}
			fields = append(fields, bc.lagPipeLogFields()...)
			fields = append(fields, senderSrcLogFields()...)
			if procPhases.Skipped > 0 {
				fields = append(fields, "skipped", procPhases.Skipped)
			}
			// S32 (docs/QS_BLOCK_TIME_BUDGET.md 6di/6dj,
			// N42_BLOCK_DECODE_REUSE_POOL): counters only, no per-block info
			// log of their own -- how many of this block's transactions were
			// reused from the pool vs freshly decoded on the block-push
			// receive path. Zero/zero for a block that took any other path
			// (gossip, catch-up fetch) or arrived with the switch off.
			if rs, ok := blk.(interface{ DecodeReuseStats() (int, int) }); ok {
				reused, decoded := rs.DecodeReuseStats()
				fields = append(fields, "reuse", reused, "dec", decoded)
			}
			// S39 (docs/QS_BLOCK_TIME_BUDGET.md 6dr/6ds): the named hand-off
			// stamps a follower's block-push receive path recorded, gated on
			// the WRITER's own N42_CONTENTION_DIAG (internal/sync's and this
			// package's own copies) -- all zero for a block that took a
			// different path (gossip, catch-up fetch) or arrived with the
			// diag off, exactly like reuse/dec above. rxEnd/decStart/decEnd
			// bracket the chunk read and full decode; chkStart/chkEnd bracket
			// CheckDeferredBlock; q is the instant the handler called
			// InsertChain; insStart is this loop's own tIter, the
			// "MISSING-STAMP" 6dr asked for -- q -> insStart is the lock/
			// queue wait between the handler and InsertChain's own
			// processing. insDispatch (S42, N42_DEFERRED_CHECK_CONCURRENT) is
			// set at the same call site as q; comparing it against
			// chkStart/chkEnd shows whether the check ran sequentially before
			// InsertChain (today) or concurrently beside it.
			if is, ok := blk.(interface {
				ImportStamps() (rxEnd, decStart, decEnd, chkStart, chkEnd, q, insDispatch, insStart int64)
			}); ok {
				rxEnd, decStart, decEnd, chkStart, chkEnd, q, insDispatch, insStart := is.ImportStamps()
				fields = append(fields,
					"rxEndTMs", rxEnd, "decStartTMs", decStart, "decEndTMs", decEnd,
					"chkStartTMs", chkStart, "chkEndTMs", chkEnd,
					"qTMs", q, "insDispatchTMs", insDispatch, "insStartTMs", insStart)
			}
			if dTotal >= slowBlockThreshold {
				log.Info("blockimport phases", fields...)
			} else {
				log.Debug("blockimport phases", fields...)
			}
		}
		if body, ok := blk.Body().(*block.Body); ok && body.IsFrameBody() {
			LogFrameSkips("import", blockNumber.Uint64(), len(blk.Transactions()), procPhases.Skipped, &procPhases.SkipReasons)
		}

		stats.processed++
		stats.usedGas += usedGas

		switch status {
		case CanonStatTy:
			log.Trace("Inserted new block ", "number ", blk.Number64(), "hash", blk.Hash(),
				"txs", len(blk.Transactions()), "gas", blk.GasUsed(),
				"elapsed", time.Since(start).Seconds(), "root", blk.StateRoot())
			if len(logs) > 0 {
				event.GlobalEvent.Send(common.NewLogsEvent{Logs: logs})
			}
			lastCanon = blk
		case SideStatTy:
			log.Debug("Inserted forked block", "number", blk.Number64(), "hash", blk.Hash(),
				"diff", blk.Difficulty(), "elapsed", time.Since(start).Seconds(),
				"txs", len(blk.Transactions()), "gas", blk.GasUsed(), "root", blk.StateRoot())
		default:
			log.Warn("Inserted block with unknown status", "number", blk.Number64(), "hash", blk.Hash(),
				"diff", blk.Difficulty(), "elapsed", time.Since(start).Seconds(),
				"txs", len(blk.Transactions()), "gas", blk.GasUsed(), "root", blk.StateRoot())
		}
	}

	// Queue remaining future blocks
	if blk != nil && errors.Is(err, ErrFutureBlock) {
		if err := bc.AddFutureBlock(blk); err != nil {
			return it.index, err
		}
		blk, err = it.next()
		for ; blk != nil && errors.Is(err, ErrUnknownAncestor); blk, err = it.next() {
			if err := bc.AddFutureBlock(blk); err != nil {
				return it.index, err
			}
			stats.queued++
		}
	}
	stats.ignored += it.remaining()
	return it.index, err
}

func (bc *BlockChain) tryZKFastPath(blk block.IBlock) bool {
	body := blk.Body()
	if body == nil || len(body.ZKProof()) == 0 {
		return false
	}

	proof, err := zkprover.DecodeProof(body.ZKProof())
	if err != nil {
		log.Debug("Failed to decode ZK proof, falling back to EVM", "block", blk.Number64(), "err", err)
		return false
	}

	if bc.zkVerifier == nil {
		return false
	}
	if !bc.zkVerifier.CryptographicReady() {
		log.Debug("ZK verifier is in side-check mode; skipping ZK fast path", "block", blk.Number64())
		return false
	}
	if err := bc.zkVerifier.Verify(proof, blk.StateRoot(), blk.GasUsed()); err != nil {
		log.Warn("ZK proof verification failed, falling back to EVM", "block", blk.Number64(), "err", err)
		return false
	}

	return true
}
