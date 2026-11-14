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
// Block building: commitWork, prepareWork, makeEnv and parent-result lookup.

package miner

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	mapset "github.com/deckarep/golang-set"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/streamverify"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/witness"
	"github.com/n42blockchain/N42/params"
)

func (w *worker) commitWork(interrupt *atomic.Int32, noempty bool, timestamp int64, parentHash types.Hash, speculative bool, triggerAt time.Time) error {
	// S75 (docs/QS_WIN2_RESIDUAL.md): tUs/parentHash let this line double as
	// both "build triggered" for THIS build and "next build triggered" for
	// the previous block's "miner: block timeline" (matched by parentHash ==
	// that timeline's sealed block hash).
	log.Info("miner: commitWork begin", "speculative", speculative, "parentHash", parentHash.Hex(), "tUs", time.Now().UnixMicro()) // diagnostic: pairs with "build triggered"
	if w.lagBuildEnabled() {
		// P2a: the build does not execute, so there is nothing to start
		// early; a speculative request is dropped.
		if speculative {
			return nil
		}
		return w.commitWorkLagged(parentHash, timestamp, triggerAt)
	}
	start := time.Now()

	// S11 diagnostics (N42_BUILD_STALL_DIAG=1; docs/QS_BLOCK_TIME_BUDGET.md
	// 6by): pf accumulates named step timings between here and the start of
	// the fill; wd dumps every goroutine's stack if the fill is not reached
	// within a few seconds. Both nil (zero cost) when the switch is off. wd
	// is disarmed by the deferred Cancel on every return path -- a normal
	// completion cancels it explicitly at the fill boundary in
	// fillTransactions; any early return here or in a callee just lets this
	// defer catch it.
	var pf *prefillTimes
	if buildStallDiagEnabled {
		pf = &prefillTimes{buildStart: start}
	}
	wd := newBuildStallWatchdog(buildStallDiagEnabled, log.LogDir())
	defer wd.Cancel()

	w.mu.RLock()
	coinbase := w.coinbase
	w.mu.RUnlock()
	if w.isRunning() && coinbase == (types.Address{}) {
		return errors.New("coinbase is empty")
	}

	// S76: classification for the "miner: build phases" line below. A real
	// speculative-HIT never reaches that line -- it returns straight to the
	// sealer a few lines down, off the fill/commit path entirely -- so
	// specStatus here only ever lands on "spec" (this call IS the speculative
	// build, speculative==true), "miss" (a parked speculative task existed for
	// parentHash but could not be used, or none was ever parked) or "none"
	// (nothing to look up: no parentHash, e.g. the first block after genesis).
	specStatus := "none"
	specReason := ""
	if speculative {
		specStatus = "spec"
	}

	// Speculative HIT: the previous view voted for parentHash, the build ran
	// during that view's vote rounds, and consensus now confirms the guess.
	// Pay the pacing wait the parked build skipped, then hand the block
	// straight to the sealer — the whole build phase is off the critical path.
	if !speculative && parentHash != (types.Hash{}) {
		if st := w.takeSpecTask(parentHash); st != nil {
			// The parked task was built on parentHash, but the applied head
			// may have moved since (another view's proposal at the same
			// height was applied locally and later abandoned). The rebuild
			// path below re-aligns the applied branch; this fast path does
			// not, so a stale seal would be rejected by
			// checkQMDBLeaderSealParent with nothing left to rebuild — the
			// view is lost. One marker read keeps the fast path honest.
			stale := false
			if bc, ok := w.chain.(*internal.BlockChain); ok && !bc.AppliedHeadIs(parentHash) {
				stale = true
			}
			if !stale {
				num := uint64(0)
				if n := st.block.Number64(); n != nil {
					num = n.Uint64()
				}
				if contentionDiagEnabled {
					st.paceEnterAt = time.Now()
				}
				if err := w.paceBlock(num); err != nil {
					return err
				}
				if contentionDiagEnabled {
					st.paceDur = time.Since(st.paceEnterAt)
					st.specHitAt = time.Now()
					st.triggerAt = triggerAt
				}
				log.Info("miner: speculative build hit", "number", num, "parent", parentHash.Hex()[:12], "tMs", time.Now().UnixMilli())
				if contentionDiagEnabled {
					st.taskChSentAt = time.Now()
				}
				select {
				case w.taskCh <- st:
				case <-w.ctx.Done():
					return w.ctx.Err()
				}
				return nil
			}
			specStatus = "miss"
			specReason = "stale-applied-head-moved"
			log.Info("miner: speculative build discarded, applied head moved", "parent", parentHash.Hex()[:12])
		} else {
			specStatus = "miss"
			specReason = "not-started"
		}
	}

	if speculative {
		// commitWork is serialized by runLoop. A confirmed matching parent
		// can wait for this build, then consume its parked task through the
		// usual applied-head and pacing checks above.
		w.activeSpecWork.Store(&newWorkReq{parentHash: parentHash, interrupt: interrupt})
		defer w.activeSpecWork.Store(nil)
	}

	// Consensus-pinned parent (HotStuff HighQC block): the world state must BE
	// that parent's post-state before we execute the payload — align it first
	// (reverts any locally-applied uncommitted sibling; no-op when already
	// aligned). AlignAppliedBranch is serialized with imports: the miner's QMDB
	// computer is isolated, so it must never peel the live tree directly. Doing
	// so can steal an importing block's in-flight undo after ComputeRoot but
	// before persistence, advancing PlainState/marker while rolling the tree
	// back one block.
	var dPersistWait time.Duration
	ownPending := false
	if bc, ok := w.chain.(*internal.BlockChain); ok {
		ownPending = w.ownPendingSpeculation(speculative, parentHash, bc)
	}
	// The layers are gathered BEFORE the read transaction opens: a parent
	// written between the two would be missing from both the store snapshot
	// and the overlay.
	var postLayers []*state.PostState
	if ownPending {
		if bc, ok := w.chain.(*internal.BlockChain); ok {
			postLayers = w.unwrittenOwnPostStates(parentHash, bc)
		}
		if postLayers == nil {
			log.Warn("miner: own unwritten parent has no post-state snapshot; waiting for its write instead",
				"parent", parentHash.Hex()[:12])
			ownPending = false
		} else {
			log.Info("miner: speculative build chains on own unwritten block",
				"parent", parentHash.Hex()[:12], "layers", len(postLayers), "accounts", postLayers[0].Accounts())
		}
	}
	if parentHash != (types.Hash{}) && !ownPending {
		if bc, ok := w.chain.(*internal.BlockChain); ok {
			// Early-vote overlap: the view can advance while the parent's
			// persistence is still in flight on this node. The build reads
			// PlainState, which lands atomically with the header — wait for it
			// rather than aligning against (and mis-reading) the pre-parent
			// state. Normally sub-millisecond; 2s covers a stalled write.
			// Persisted, then AlignAppliedBranch below: the align is what
			// unwinds a locally-applied sibling so the consensus parent can
			// be imported. Waiting here for the parent to be APPLIED
			// (580d2f32) ran before that unwind and could never succeed on a
			// node that had applied a sibling -- round 35p: every view of a
			// tenure timed out on "consensus parent not applied in time".
			wd.SetStep("persistWait")
			tPersist := time.Now()
			if !bc.WaitBlockPersisted(parentHash, 2*time.Second) {
				return fmt.Errorf("consensus parent %x not persisted in time", parentHash[:8])
			}
			// Round 35z9: `align` was 551 ms of a 1,358 ms build at 163k
			// transactions, on the path between one commit and the next
			// proposal, and there was no way to tell the wait for the parent's
			// write from the unwind that follows it. Two numbers, so the next
			// round knows which half to attack.
			dPersistWait = time.Since(tPersist)
			if pf != nil {
				pf.persistWait = dPersistWait
			}
			pblk, _ := w.chain.GetBlockByHash(parentHash)
			if pblk == nil {
				return fmt.Errorf("consensus parent %x not in local db", parentHash[:8])
			}
			// Nothing to unwind when the applied state is exactly the
			// consensus parent, which is every block of a healthy chain.
			// AlignAppliedBranch takes bc.lock and so waits behind the
			// parent's own write; skipping it there took 230 ms off the
			// leader's 512 ms align (round 35za).
			alignNeeded := !bc.AppliedHeadIsExactly(parentHash, pblk.Number64().Uint64())
			wd.SetStep("alignAppliedBranch")
			tAlignCall := time.Now()
			if err := func() error {
				if !alignNeeded {
					return nil
				}
				return bc.AlignAppliedBranch(pblk.Number64().Uint64()+1, parentHash)
			}(); err != nil {
				if pf != nil {
					pf.align += time.Since(tAlignCall)
					pf.lockWait += bc.TakeBuildStallLockWait()
				}
				if speculative {
					// A guess is not worth a forced import: the align fallback
					// below re-imports the consensus parent with switch
					// authority, which is only justified when consensus has
					// actually mandated this parent. Give up quietly.
					return fmt.Errorf("speculative align failed: %w", err)
				}
				// The QC block is in the DB but on a branch this node never
				// applied (it didn't vote in that view). Import it WITH switch
				// authority — the QC is the consensus mandate — then re-align.
				wd.SetStep("insertChainAuthorized")
				tInsert := time.Now()
				_, ierr := bc.InsertChainAuthorized([]block.IBlock{pblk})
				if pf != nil {
					pf.insertParent += time.Since(tInsert)
					pf.lockWait += bc.TakeBuildStallLockWait()
				}
				if ierr != nil {
					return fmt.Errorf("import consensus parent %x: %w (align: %v)", parentHash[:8], ierr, err)
				}
				wd.SetStep("alignAppliedBranch")
				tAlignCall2 := time.Now()
				err = bc.AlignAppliedBranch(pblk.Number64().Uint64()+1, parentHash)
				if pf != nil {
					pf.align += time.Since(tAlignCall2)
					pf.lockWait += bc.TakeBuildStallLockWait()
				}
				if err != nil {
					return fmt.Errorf("align applied branch to consensus parent %x: %w", parentHash[:8], err)
				}
			} else if pf != nil {
				pf.align += time.Since(tAlignCall)
				pf.lockWait += bc.TakeBuildStallLockWait()
			}
			// The align is a no-op when the applied head is BELOW the parent
			// (unwindForReimport leaves "not applied yet" to the future queue),
			// and the build would then run on the wrong base. Round 35zg B1:
			// after the leg's restart node6's startup revert sat at 14029922,
			// consensus named 14029923 as the parent, the speculative build of
			// 14029924 ran on a tree reloaded at 14029922, the parent was
			// re-imported one second later, the parked task matched by hash and
			// the fleet rejected an empty block with three different roots --
			// the OPEN_ISSUES "three roots after a restart" shape. Never build on
			// a parent that is not the applied head: a speculative build gives
			// up (the leader gate's deferred resume re-triggers after the
			// parent applies); a production build imports the parent with
			// switch authority first and checks again.
			if !bc.AppliedHeadIsExactly(parentHash, pblk.Number64().Uint64()) {
				if speculative {
					return fmt.Errorf("speculative build: consensus parent %x not applied yet", parentHash[:8])
				}
				wd.SetStep("insertChainAuthorized")
				tInsert2 := time.Now()
				_, ierr := bc.InsertChainAuthorized([]block.IBlock{pblk})
				if pf != nil {
					pf.insertParent += time.Since(tInsert2)
					pf.lockWait += bc.TakeBuildStallLockWait()
				}
				if ierr != nil {
					return fmt.Errorf("import consensus parent %x before building: %w", parentHash[:8], ierr)
				}
				if !bc.AppliedHeadIsExactly(parentHash, pblk.Number64().Uint64()) {
					return fmt.Errorf("consensus parent %x still not the applied head after import", parentHash[:8])
				}
			}
		}
	}

	wd.SetStep("prepareWork")
	tPrepareWork := time.Now()
	current, err := w.prepareWork(&generateParams{timestamp: uint64(timestamp), coinbase: coinbase, parentHash: parentHash})
	if pf != nil {
		pf.headerPrepare = time.Since(tPrepareWork)
	}
	if err != nil {
		log.Error("cannot prepare work", "err", err)
		return err
	}
	if n := current.header.Number; n != nil {
		wd.SetBlockNumber(n.Uint64())
	}

	// Block-production pacing (skipped for speculative builds, which run early
	// on purpose; the grid wait is paid when the parked block is HANDED OVER,
	// see the speculative-hit path in this function's caller flow).
	var tPaceEnter time.Time
	var dPace time.Duration
	if !speculative {
		if contentionDiagEnabled {
			tPaceEnter = time.Now()
		}
		if err := w.paceBlock(current.header.Number.Uint64()); err != nil {
			return err
		}
		if contentionDiagEnabled {
			dPace = time.Since(tPaceEnter)
		}
	}

	tAlign := time.Since(start)
	wd.SetStep("roTxBegin")
	tRoTx := time.Now()
	tx, err := w.chain.DB().BeginRo(w.ctx)
	if pf != nil {
		pf.roTxBegin = time.Since(tRoTx)
	}
	if err != nil {
		log.Error("work.commitWork failed", err)
		return err
	}
	defer tx.Rollback()

	var stateReader state.StateReader = state.NewPlainStateReader(tx)
	if cache := layered.ExtractCache(w.chain.DB()); cache != nil {
		stateReader = state.NewCachedStateReader(stateReader, cache)
	}
	// A chained build reads its unwritten own ancestors' effects from their
	// post-state snapshots, oldest innermost (round 35zr: without this the
	// coinbase was credited on the pre-parent balance and the root diverged).
	for i := len(postLayers) - 1; i >= 0; i-- {
		stateReader = state.NewPostStateReader(postLayers[i], stateReader)
	}

	// Under the recorder and the tracer: the parallel fill seeds this layer
	// with the delta-credited recipients read across its workers, and the
	// block-end fold's reads -- logged above it in the same order as before
	// -- become map hits.
	prefetch := state.NewAccountPrefetch(stateReader)
	stateReader = prefetch
	current.untracedReader = prefetch

	// Wrap state reader with TracingReader when JMT is enabled to record
	// all state accesses for witness generation.
	var tracingReader *witness.TracingReader
	if bc, ok := w.chain.(*internal.BlockChain); ok && bc.IsJMTEnabled() {
		tracingReader = witness.NewTracingReader(stateReader)
		stateReader = tracingReader
	}

	// Wrap with the mobile-verification read-log recorder when a packet
	// sink is wired (docs/mobile-attestation-design.md §4). Same observer
	// pattern as TracingReader above: capture-only, execution unchanged.
	var readLogRecorder *streamverify.ReadLogRecorder
	if w.mobilePacketSink != nil {
		readLogRecorder = streamverify.NewReadLogRecorder(stateReader, nil)
		stateReader = readLogRecorder
	}

	stateWriter := state.NewNoopWriter()
	ibs := state.New(stateReader)
	// Carried explicitly: the parallel fill's per-worker readers must layer
	// the same snapshots, and the recorder wrappers above hide them from a
	// walk of the reader chain.
	ibs.SetPostStateLayers(postLayers)
	ibs.SetAccountPrefetch(prefetch)
	// Inject an isolated root computer so the assembled block's stateRoot uses
	// the active commitment scheme (e.g. QMDB twig forest). Speculative builds
	// must not mutate the live tree, so this is a fresh DB-backed instance,
	// discarded with the block. Without it, IntermediateRoot falls back to an
	// empty MPT root and the produced block can never become canonical.
	if bcForRoot, ok := w.chain.(*internal.BlockChain); ok {
		// The parent's state root lets the miner tree recognise that the block
		// it built last IS this build's parent (NewMinerRootComputer's
		// own-block fast path).
		// The parent's EXECUTED root: its header's Root before the fork, this
		// node's result of it under deferred execution (the header then
		// carries the grandparent's).
		parentRoot := types.Hash{}
		if current.header.Number.Uint64() > 0 {
			if pr, perr := w.parentExecutedResult(tx, current.header); perr == nil {
				parentRoot = pr.Root
			} else if w.chainConfig.IsDeferredExecution(current.header.Time) {
				return fmt.Errorf("miner: deferred execution: %w", perr)
			}
		}
		wd.SetStep("specTreeReload")
		tReloadCall := time.Now()
		rc, rcErr := bcForRoot.NewMinerRootComputer(tx, parentRoot)
		if pf != nil {
			pf.specTreeReload += time.Since(tReloadCall)
			pf.rootLockWait += bcForRoot.TakeBuildStallRootLockWait()
		}
		if rcErr != nil {
			// A block sealed on the fallback (live or empty) root can never
			// become canonical; abandon the build instead of proposing it.
			return fmt.Errorf("miner: speculative root computer unavailable: %w", rcErr)
		}
		if rc != nil {
			ibs.SetRootComputer(rc)
		}
	}
	tReload := time.Since(start)
	ibs.BeginWriteSnapshot()
	ibs.BeginWriteCodes()

	var headers []*block.Header
	getHeader := func(hash types.Hash, number uint64) *block.Header {
		h := rawdb.ReadHeader(tx, hash, number)
		if h != nil {
			headers = append(headers, h)
		}
		return h
	}

	// Start-of-block system operations (EIP-4788 beacon root, EIP-2935 parent
	// blockhash ring buffer) — the SAME hooks StateProcessor.Process runs on
	// the import side. Building without them produced a state root missing the
	// system contracts' storage writes, so with import-side root verification
	// on, every follower rejected every locally built block (proposer/computed
	// mismatch on the very first sealed block).
	wd.SetStep("blockStartSyscalls")
	tBlockStart := time.Now()
	if err := internal.ProcessExecutionBlockStart(current.header.ParentBeaconRoot, w.chainConfig, ibs, current.header, w.engine); err != nil {
		return fmt.Errorf("miner block-start system calls: %w", err)
	}
	if pf != nil {
		pf.blockStart = time.Since(tBlockStart)
	}

	// N42 native chain (phase 6c): stamp the committed mobile-registry root into
	// the header when the MobileAnchor fork is active. This is a HEADER
	// commitment only — like the CommitteePool's ParentBeaconRoot link, it binds
	// the root into the block hash (RLP) with NO state-trie write, so it needs no
	// system contract, no genesis alloc and no replay to activate. The full
	// history lives in the rawdb anchor log (phase 6b). Gated on IsMobileAnchor;
	// a nil provider or inactive fork leaves the header field nil (byte-identical
	// pre-fork / on eth-el).
	if w.mobileAnchorRoot != nil && w.chainConfig.IsMobileAnchor(current.header.Time) {
		current.header.MobileRegistryRoot = w.mobileAnchorRoot()
	}

	tPrep := time.Since(start)
	wd.SetStep("pendingSnapshot")
	tl := &fillTimeline{}
	err = w.fillTransactions(interrupt, current, ibs, getHeader, pf, wd, tl)
	switch {
	case err == nil:
		w.resubmitAdjustCh <- &intervalAdjust{inc: false}
	case errors.Is(err, errBlockInterruptedByRecommit):
		gaslimit := current.header.GasLimit
		ratio := float64(gaslimit-current.gasPool.Gas()) / float64(gaslimit)
		if ratio < 0.1 {
			ratio = 0.1
		}
		w.resubmitAdjustCh <- &intervalAdjust{
			ratio: ratio,
			inc:   true,
		}
	default:
		return fmt.Errorf("fill transactions: %w", err)
	}

	// End-of-block system calls (EIP-7002 withdrawal / EIP-7251 consolidation
	// requests) — mirror of StateProcessor.Process before Finalize, for the
	// same build/verify state-root equivalence as the block-start hooks above.
	if _, err := internal.ProcessExecutionBlockEnd(nil, w.chainConfig, ibs, current.header, w.engine); err != nil {
		return fmt.Errorf("miner block-end system calls: %w", err)
	}

	// Build-phase breakdown: the dropped-seal hunt found ~6s builds with a
	// 6ms seal and no visible spender - this line is the missing evidence.
	//
	// S75 (docs/QS_WIN2_RESIDUAL.md): fillTx used to be one opaque number
	// bundling pool-scan + selection + EVM execution; fillScan/fillExec
	// split it using tl.FillExecDur (the loop's accumulated ApplyTransaction
	// time, dCommit in fillTransactions -- zero on the parallel-fill path or
	// an empty-pool early return, in which case fillScan==fillTx and
	// fillExec==0). poolSnapshotUs/fillLoopDoneUs are 0 when tl was never
	// populated (same early-return cases).
	fillTxTotal := time.Since(start) - tPrep
	fillExec := tl.FillExecDur
	fillScan := fillTxTotal - fillExec
	var poolSnapshotUs, fillLoopDoneUs int64
	if !tl.PoolSnapshotAt.IsZero() {
		poolSnapshotUs = tl.PoolSnapshotAt.UnixMicro()
	}
	if !tl.FillLoopDoneAt.IsZero() {
		fillLoopDoneUs = tl.FillLoopDoneAt.UnixMicro()
	}
	log.Info("miner: build phases",
		"align", tAlign, "persistWait", dPersistWait, "reload", tReload-tAlign, "syscalls", tPrep-tReload,
		"fillTx", fillTxTotal, "total", time.Since(start),
		"fillScan", fillScan, "fillExec", fillExec,
		"candidatesScanned", tl.PendingTxCount, "candidatesIncluded", current.tcount,
		"poolSnapshotUs", poolSnapshotUs, "fillLoopDoneUs", fillLoopDoneUs,
		"buildTriggeredUs", triggerAt.UnixMicro(), "parentHash", parentHash.Hex(),
		// S76 (docs/QS_TIMELINE_35zzzba.md): a real speculative-HIT never
		// reaches this line (it returns to the sealer earlier in commitWork),
		// so "spec" here distinguishes a speculative build in flight ("spec")
		// from an on-critical-path build that missed a parked speculative task
		// ("miss", with specReason) or had none to look up ("none").
		"spec", specStatus, "specReason", specReason,
		// tMs is the build's END; tMs - total is its start. Round 35zzq could not
		// place either against the QC, and the gap between them is the cycle now.
		"tMs", time.Now().UnixMilli())
	// w.commit() is the rest of commitWork: it assembles and finalizes the
	// block, creates the task and hands it to taskCh, where taskLoop stamps
	// sealStart. Everything between "build phases" (logged immediately above)
	// and sealStart is therefore in here, and that is the remainder of the
	// unaccounted 247-373 ms once the gates (0.03 ms) and the work queue
	// (0.01 ms) are ruled out. task.assemble and task.finalize are recorded
	// INSIDE this call, so they are before sealStart, not inside seal2res --
	// an earlier subtraction in docs/QS_BLOCK_TIME_BUDGET.md had them on the
	// wrong side.
	tCommit := time.Now()
	err = w.commit(current, stateWriter, ibs, start, headers, tracingReader, readLogRecorder, speculative, parentHash, triggerAt, tPaceEnter, dPace)
	log.Info("miner: commit phases", "speculative", speculative, "commitNs", time.Since(tCommit).Nanoseconds())
	if err != nil {
		log.Errorf("w.commit failed, error %v\n", err)
		return err
	}

	return nil
}

func (w *worker) prepareWork(param *generateParams) (*environment, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	timestamp := param.timestamp

	currentBlock := w.chain.CurrentBlock()
	if currentBlock == nil {
		return nil, errors.New("current block is nil")
	}
	currentHeader := currentBlock.Header()
	if currentHeader == nil {
		return nil, errors.New("current block header is nil")
	}
	parent, ok := currentHeader.(*block.Header)
	if !ok || parent == nil {
		return nil, errors.New("invalid current block header type")
	}
	if param.parentHash != (types.Hash{}) {
		b := w.ownSealed(param.parentHash)
		if b == nil {
			b, _ = w.chain.GetBlockByHash(param.parentHash)
		}
		if b == nil {
			return nil, errors.New("missing parent")
		}
		parent, ok = b.Header().(*block.Header)
		if !ok || parent == nil {
			return nil, errors.New("invalid parent header type")
		}
	}

	if parent.Time >= param.timestamp {
		timestamp = parent.Time + 1
	}

	parentNumber, err := requireHeaderNumber(parent, "parent block number unavailable")
	if err != nil {
		return nil, err
	}

	header := &block.Header{
		ParentHash: parent.Hash(),
		Coinbase:   param.coinbase,
		Number:     uint256.NewInt(0).Add(parentNumber, uint256.NewInt(1)),
		GasLimit:   CalcGasLimit(parent.GasLimit, w.minerConf.GasCeil),
		Time:       uint64(timestamp),
		Difficulty: uint256.NewInt(0),
		BaseFee:    uint256.NewInt(0),
	}

	// Set baseFee and GasLimit if we are on an EIP-1559 chain
	headerNumber, err := requireHeaderNumber(header, "mining header number unavailable")
	if err != nil {
		return nil, err
	}
	if w.chainConfig.IsLondon(headerNumber.Uint64()) {
		header.BaseFee, _ = uint256.FromBig(misc.CalcBaseFee(w.chainConfig, parent))
		if !w.chainConfig.IsLondon(parentNumber.Uint64()) {
			parentGasLimit := parent.GasLimit * params.ElasticityMultiplier
			header.GasLimit = CalcGasLimit(parentGasLimit, w.minerConf.GasCeil)
		}
	}

	if err := w.engine.Prepare(w.chain, header); err != nil {
		return nil, err
	}

	return w.makeEnv(parent, header, param.coinbase), nil
}

func (w *worker) makeEnv(parent *block.Header, header *block.Header, coinbase types.Address) *environment {
	env := &environment{
		ancestors: mapset.NewSet(),
		family:    mapset.NewSet(),
		coinbase:  coinbase,
		header:    header,
		gasPool:   new(common.GasPool).AddGas(fillGasBudget(header.GasLimit)),
	}

	for _, ancestor := range w.chain.GetBlocksFromHash(parent.ParentHash, 3) {
		env.family.Add(ancestor.Hash())
		env.ancestors.Add(ancestor.Hash())
	}

	return env
}

// parentExecutedResult returns this node's execution result of the parent
// of header: the record kept with a block this node sealed, else the
// stored result of an applied block (or the parent header's own fields
// before the deferred-execution fork).
func (w *worker) parentExecutedResult(tx kv.Getter, header *block.Header) (rawdb.ExecutedResult, error) {
	parent := header.ParentHash
	w.mu.RLock()
	r, ok := w.sealedExec[parent]
	ownBlk := w.sealedByHash[parent]
	w.mu.RUnlock()
	if ok {
		return r, nil
	}
	var ph *block.Header
	if ownBlk != nil {
		ph, _ = ownBlk.Header().(*block.Header)
	}
	if ph == nil && header.Number.Uint64() > 0 {
		ph = rawdb.ReadHeader(tx, parent, header.Number.Uint64()-1)
	}
	if ph == nil {
		return rawdb.ExecutedResult{}, fmt.Errorf("parent %x of block %d unknown", parent[:8], header.Number.Uint64())
	}
	return internal.ExecutedResultOfHeader(w.chainConfig, tx, ph)
}
