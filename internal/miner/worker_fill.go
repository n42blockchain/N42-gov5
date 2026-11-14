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
// Transaction filling and miner block timeline logging.

package miner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/miner/builder"
	"github.com/n42blockchain/N42/internal/streamverify"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// parallelFillEnabled gates the builder's Block-STM fill (N42_MINER_PARALLEL_FILL=1).
func parallelFillEnabled() bool { return os.Getenv("N42_MINER_PARALLEL_FILL") == "1" }

// markReadLogDiverged flags the build's mobile read log (the recorder is the
// IntraBlockState's own reader when a packet sink is wired) as no longer a
// replay of the included transactions. A candidate that read state and was
// then reverted and left out keeps its reads in the log, and the objects it
// loaded stay cached in the IntraBlockState, so a later included transaction
// touching them reads nothing; verifiers, which consume the log by position
// and never run the candidate, would read every later entry shifted. The
// packet job rebuilds a flagged log from the recorded values (mobilePacketJob
// .readLog), off the critical path. Execution is not touched: forgetting the
// cached objects instead would put consensus state behind a read-log fix.
func markReadLogDiverged(ibs *state.IntraBlockState) {
	if rec, ok := ibs.GetStateReader().(*streamverify.ReadLogRecorder); ok {
		rec.MarkDiverged()
	}
}

// staleTrimReader is the reader the stale-nonce trim reads accounts through:
// the build's untraced reader when it has one, else the state's own reader.
func staleTrimReader(env *environment, ibs *state.IntraBlockState) state.StateReader {
	if env != nil && env.untracedReader != nil {
		return env.untracedReader
	}
	return ibs.GetStateReader()
}

// pf and wd are S11's diagnostic pre-fill timer/watchdog (both nil unless
// N42_BUILD_STALL_DIAG=1; docs/QS_BLOCK_TIME_BUDGET.md 6by). This function
// is where the pre-fill total completes (pending-pool snapshot + stale
// trim) and where the parallel/serial fill decision is made, so it is also
// where pf's summary line is logged and wd is cancelled -- the literal
// "start of the fill" boundary.
// fillTimeline carries S75's timeline stamps out of fillTransactions
// (docs/QS_WIN2_RESIDUAL.md) without touching any of its many return
// statements: PoolSnapshotAt is stamped right after w.txsPool.Pending()
// returns ("pool snapshot taken"), FillLoopDoneAt right after the serial
// selection loop exits ("fill loop done"), FillExecDur is the accumulated
// ApplyTransaction time (dCommit) inside that loop, and PendingTxCount is
// the scanned-candidate count (pendingTxs). All four are left zero-value on
// the parallel-fill path and on early "no pending transactions" returns
// before the respective point is reached -- the caller treats a zero
// PoolSnapshotAt/FillLoopDoneAt as "not reached" rather than as a real
// microsecond-zero timestamp.
type fillTimeline struct {
	PoolSnapshotAt time.Time
	FillLoopDoneAt time.Time
	FillExecDur    time.Duration
	PendingTxCount int
}

// minerBlockTimelineJSON is S75's one-JSON-line-per-block leader timeline
// (docs/QS_WIN2_RESIDUAL.md). finalizeDoneUs/rootDoneUs share one timestamp
// today -- the leader computes the state root inside FinalizeAndAssemble and
// does not time it separately from Finalize, unlike the follower's
// "blockimport phases" root field; see the call site's comment.
type minerBlockTimelineJSON struct {
	Number           uint64 `json:"number"`
	Hash             string `json:"hash"`
	BuildTriggeredUs int64  `json:"buildTriggeredUs"`
	FinalizeDoneUs   int64  `json:"finalizeDoneUs"`
	RootDoneUs       int64  `json:"rootDoneUs"`
	BlockSealedUs    int64  `json:"blockSealedUs"`
	Txs              int    `json:"txs"`
}

// logMinerBlockTimeline emits the leader-side "miner: block timeline" Info
// line (S75, docs/QS_WIN2_RESIDUAL.md). Marshal failure is not possible for
// this fixed, all-primitive struct, but is handled defensively since this is
// a logging-only addition that must never affect block production.
func logMinerBlockTimeline(iblock block.IBlock, triggerAt, finalizeDoneAt, rootDoneAt, sealedAt time.Time, txs int) {
	num := uint64(0)
	if n := iblock.Number64(); n != nil {
		num = n.Uint64()
	}
	tl := minerBlockTimelineJSON{
		Number:           num,
		Hash:             iblock.Hash().Hex(),
		BuildTriggeredUs: triggerAt.UnixMicro(),
		FinalizeDoneUs:   finalizeDoneAt.UnixMicro(),
		RootDoneUs:       rootDoneAt.UnixMicro(),
		BlockSealedUs:    sealedAt.UnixMicro(),
		Txs:              txs,
	}
	b, err := json.Marshal(tl)
	if err != nil {
		log.Warn("miner: block timeline marshal failed", "err", err)
		return
	}
	log.Info("miner: block timeline", "role", "leader", "json", string(b))
}

func (w *worker) fillTransactions(interrupt *atomic.Int32, env *environment, ibs *state.IntraBlockState, getHeader func(hash types.Hash, number uint64) *block.Header, pf *prefillTimes, wd *buildStallWatchdog, tl *fillTimeline) (retErr error) {
	header := env.header
	headerNumber, err := requireHeaderNumber(header, "mining header number unavailable")
	if err != nil {
		return err
	}

	// Pre-allocate with estimated max tx count to avoid append-growth
	// in the hot commit loop. 21000 is the minimum gas per tx. The estimate is
	// exact for a transfer-saturated block (480M/21000 = 22,857 slots, ~360KB
	// across both slices); the old 1024 cap forced five doubling copies per
	// full block to save memory nobody was short of.
	estCap := int(env.gasPool.Gas() / 21000)
	if estCap > 1<<20 {
		estCap = 1 << 20 // sanity bound for absurd gas limits
	}
	env.txs = make([]*transaction.Transaction, 0, estCap)
	env.receipts = make(block.Receipts, 0, estCap)

	// Bound the packed size so the sealed block can actually propagate. Gas is
	// NOT a proxy for wire size: ~9500 plain transfers fit comfortably inside a
	// raised gas limit yet encode to >1 MiB, above the p2p bound, and a block no
	// follower can receive livelocks import-gated HotStuff voting forever. See
	// block_size.go.
	sizeLimiter := newBlockSizeLimiter(header)

	noop := state.NewNoopWriter()
	vmConfig := vm2.Config{}
	coinbase := env.coinbase
	blockContext := internal.NewEVMBlockContext(header, internal.GetHashFn(header, getHeader), w.engine, w.chainConfig, &coinbase)
	vmenv := vm2.NewEVM(blockContext, evmtypes.TxContext{}, ibs, w.chainConfig, vmConfig)

	// EIP-7928: when the BAL fork is active, harvest each committed tx's post-value
	// writes via the shared BALCapture (a pure observer wrapping noop) so the block
	// access list hash can be bound into the header below. The importer recomputes
	// it the same way (StateProcessor.Process), so the two agree by construction.
	// balCap is nil (zero overhead) when the fork is off.
	var writer state.StateWriter = noop
	var balCap *internal.BALCapture
	if w.chainConfig.IsBAL(env.header.Time) {
		balCap = internal.NewBALCapture(noop)
		writer = balCap
	}
	finalizeBAL := func() error {
		if balCap == nil {
			return nil
		}
		h, err := balCap.HashFor(internal.TxHashes(env.txs))
		if err != nil {
			return fmt.Errorf("compute block access list hash: %w", err)
		}
		env.header.BlockAccessListHash = &h
		return nil
	}
	defer func() {
		if err := finalizeBAL(); err != nil && retErr == nil {
			retErr = err
		}
	}()
	// From Amsterdam the header carries the EIP-8037 block figure,
	// max(cumulative execution gas, cumulative state gas), not the receipts'
	// cumulative gas; import checks it that way.
	defer func() {
		header.GasUsed = internal.HeaderGasUsed(w.chainConfig, header, env.gasPool, header.GasUsed)
	}()

	commitTx := func(txn *transaction.Transaction) error {
		ibs.Prepare(txn.Hash(), types.Hash{}, env.tcount)
		// A struct copy: the Amsterdam block counters must survive a revert.
		gasSnap := *env.gasPool
		gasUsedSnap := header.GasUsed
		snap := ibs.Snapshot()
		log.Debug("addTransactionsToMiningBlock", "txn hash", txn.Hash())

		if balCap != nil {
			balCap.BeginTx(txn.Hash())
		}
		receipt, _, err := internal.ApplyTransactionWithEVM(vmenv, w.chainConfig, w.engine, env.gasPool, ibs, writer, env.header, txn, &header.GasUsed, vmConfig)
		if err != nil {
			if balCap != nil {
				balCap.DiscardTx()
			}
			// Whatever the failure, the candidate may have read state that
			// stays logged and cached after the revert (see
			// markReadLogDiverged).
			markReadLogDiverged(ibs)
			ibs.RevertToSnapshot(snap)
			*env.gasPool = gasSnap
			header.GasUsed = gasUsedSnap
			return err
		}
		if balCap != nil {
			balCap.CommitTx()
		}

		env.txs = append(env.txs, txn)
		env.receipts = append(env.receipts, receipt)
		return nil
	}

	blockNumber := headerNumber.Uint64()

	// Frame-referenced blocks take whole frames from the frame store instead
	// of bundles and pool transactions (docs/QS_MULTI_MILLION_DESIGN.md P1b).
	if internal.FrameBlocksEnabled() {
		wd.Cancel()
		return w.fillFrames(env, ibs, getHeader, commitTx, balCap == nil, tl)
	}

	// Phase 1: Execute MEV bundles at block top (highest-paying first).
	if w.bundlePool != nil {
		wd.SetStep("mevBundles")
		bundles := w.bundlePool.GetBundles(blockNumber, header.Time)
		for _, bundle := range bundles {
			if env.gasPool.Gas() < params.TxGas {
				break
			}
			// Size-account the whole bundle up front: it is committed
			// atomically, so it must fit atomically. The estimate is an upper
			// bound (revert-allowed transactions that fail are dropped from the
			// block but still counted), which only ever under-packs.
			bundleSize, sizeErr := bundlePayloadSize(bundle.Txs)
			if sizeErr != nil {
				log.Warn("skipping unencodable MEV bundle", "err", sizeErr)
				continue
			}
			if !sizeLimiter.fits(bundleSize) {
				if sizeLimiter.exhausted() {
					break
				}
				continue
			}
			// Try to commit entire bundle atomically.
			snap := ibs.Snapshot()
			gasSnap := *env.gasPool
			gasUsedSnap := header.GasUsed
			startTxCount := len(env.txs)
			startReceiptCount := len(env.receipts)
			startTCount := env.tcount
			balSnap := 0
			if balCap != nil {
				balSnap = balCap.Snapshot()
			}
			bundleOk := true

			for _, tx := range bundle.Txs {
				err := commitTx(tx)
				if err != nil {
					if bundle.IsRevertAllowed(tx.Hash()) {
						continue // Allowed to fail
					}
					bundleOk = false
					break
				}
				env.tcount++
			}

			if bundleOk {
				sizeLimiter.add(bundleSize)
			} else {
				// Revert entire bundle.
				markReadLogDiverged(ibs)
				ibs.RevertToSnapshot(snap)
				*env.gasPool = gasSnap
				header.GasUsed = gasUsedSnap
				env.txs = env.txs[:startTxCount]
				env.receipts = env.receipts[:startReceiptCount]
				env.tcount = startTCount
				if balCap != nil {
					balCap.RevertToSnapshot(balSnap)
				}
			}
		}
	}

	// Phase 2: Fill remaining space with regular transactions sorted by effective tip.
	wd.SetStep("pendingSnapshot")
	tPending := time.Now()
	pending := w.txsPool.Pending(false)
	dPending := time.Since(tPending)
	if tl != nil {
		tl.PoolSnapshotAt = time.Now() // S75: "pool snapshot taken"
	}
	pendingRawAccts := len(pending)
	if len(pending) == 0 {
		pf.logIfSlow(blockNumber, dPending, 0)
		wd.Cancel()
		return nil
	}

	// Round 28: after a large block lands the pool's reorg lags the
	// speculative build by up to 1.7 s, and the build then EXECUTES the
	// previous block's mined transactions one by one (ErrNonceTooLow, ~5 us
	// each, ~0.7 s for a 163k block) before reaching anything fresh. Read
	// each account's state nonce once and drop the stale prefix here
	// instead: O(accounts) reads for O(stale transactions) executions saved.
	// Semantics unchanged -- those transactions fail the same way inside.
	// Read through the state READER, never the IntraBlockState: a read there
	// creates a state object, and a touched-but-unchanged object is one more
	// no-op entry in an append-only commitment -- a root the followers, who
	// never touched it, cannot reproduce. And read below the mobile read-log
	// recorder (env.untracedReader): these reads are not part of the block.
	wd.SetStep("staleTrim")
	tTrim := time.Now()
	staleTrimmed := trimStalePending(staleTrimReader(env, ibs), pending)
	dTrim := time.Since(tTrim)
	if len(pending) == 0 {
		pf.logIfSlow(blockNumber, dPending, dTrim)
		wd.Cancel()
		return nil
	}
	txSet := builder.NewTxByPriceAndNonce(pending, header.BaseFee)

	// S11: this is the true "start of the fill" boundary -- everything above
	// is pre-fill setup (align/import, RoTx open, tree reload, header
	// prepare, pool snapshot, stale trim); everything below (parallel or
	// serial) is the fill itself. Log the summary and disarm the watchdog
	// here, whichever fill path runs next.
	pf.logIfSlow(blockNumber, dPending, dTrim)
	wd.Cancel()

	// Round 35k: the builder's fill was the leader's largest single phase at
	// 163k transactions (commit 611-683 ms, ~3.8 us/tx serial), with the
	// import already on Block-STM at ~200 ms. Pick the candidates in the
	// same price-and-nonce order without executing (gas by each
	// transaction's limit, so the block cannot exceed the ceiling), run them
	// through the same executor the followers use, drop the ones that fail
	// their pre-check (they wrote nothing; a dropped nonce fails its sender's
	// later candidates the same way), and hand the survivors to assemble.
	// Bundles, BAL capture and fee-recipient candidates keep the serial path.
	if parallelFillEnabled() && balCap == nil && env.tcount == 0 && !w.chainConfig.IsBAL(env.header.Time) && !w.chainConfig.IsGlamsterdam(env.header.Time) {
		if bc, ok := w.chain.(*internal.BlockChain); ok {
			tPickP := time.Now()
			budget := env.gasPool.Gas()
			candidates := make([]*transaction.Transaction, 0, estCap)
			for {
				tx := txSet.Peek()
				if tx == nil {
					break
				}
				if tx.Gas() > budget {
					txSet.Pop()
					if budget < params.TxGas {
						break
					}
					continue
				}
				txSize, decision, sizeErr := sizeLimiter.admit(tx)
				if sizeErr != nil || decision != packAccept {
					if decision == packStop {
						break
					}
					txSet.Pop() // packSkipAccount, or unencodable: skip the account
					continue
				}
				sizeLimiter.add(txSize)
				budget -= tx.Gas()
				candidates = append(candidates, tx)
				txSet.Shift()
			}
			dPickP := time.Since(tPickP)
			// The workers read through their own readers, around the
			// recorder, and drop candidates that fail their pre-check.
			markReadLogDiverged(ibs)
			tRun := time.Now()
			included, receipts, usedGas, failed, perr := bc.BuildParallel(header, candidates, ibs, internal.GetHashFn(header, getHeader))
			if perr == nil {
				env.txs = append(env.txs, included...)
				env.receipts = append(env.receipts, receipts...)
				env.tcount += len(included)
				header.GasUsed += usedGas
				if err := env.gasPool.SubGas(usedGas); err != nil {
					return err
				}
				dRunP := time.Since(tRun)
				// S76: the parallel-fill success path used to `return nil` here
				// without ever touching tl, so fillLoopDoneUs/fillExec/
				// candidatesScanned stayed at their zero value on EVERY build
				// under the standing N42_MINER_PARALLEL_FILL=1 config -- the
				// serial loop below (which does stamp tl) never runs when this
				// branch succeeds. Mirror the same three stamps here: pick+size
				// admission is this path's "scan" (fillScan = fillTx - fillExec
				// at the call site), BuildParallel's wall time is its "exec".
				if tl != nil {
					tl.FillLoopDoneAt = time.Now()
					tl.FillExecDur = dRunP
					tl.PendingTxCount = len(candidates)
				}
				log.Info("miner: parallel fill", "candidates", len(candidates), "included", len(included), "failed", failed,
					"pick", dPickP, "run", dRunP, "pendingSnapshot", dPending, "trim", dTrim, "staleTrimmed", staleTrimmed)
				return nil
			}
			if !errors.Is(perr, internal.ErrParallelNotApplicable) {
				return perr
			}
			// Not applicable: rebuild the set and take the serial path.
			txSet = builder.NewTxByPriceAndNonce(pending, header.BaseFee)
			sizeLimiter = newBlockSizeLimiter(header)
		}
	}
	log.Tracef("fillTransactions pending accounts:%d", len(pending))
	// Round 27: a block that comes out a fifth full with a pool the harness
	// reports at 200,000 pending is only diagnosable if the build says what
	// the POOL handed it. Counted once per build, reported on the breakdown.
	pendingTxs := 0
	for _, list := range pending {
		pendingTxs += len(list)
	}
	if tl != nil {
		tl.PendingTxCount = pendingTxs // S75: "candidates scanned"
	}

	// Accounts skipped because their head transaction cannot pay the base fee.
	// Reported once per build: a block that comes out empty with a full pool is
	// otherwise indistinguishable from a block that had nothing to include, and
	// the difference is the whole diagnosis.
	priceOut := 0
	// Why accounts left the set (round 28: a 10k pack from a 449k pool).
	var popGas, popNonceHigh, skipNonceLow, popOther int
	lookupWait0 := commitment.QMDBLookupWaitNanos()
	// fillTx phase accumulators — see the breakdown log below the loop.
	var dPick, dCommit, dHeap time.Duration

	for {
		if interrupt != nil {
			if signal := interrupt.Load(); signal != commitInterruptNone {
				// The deferred finalizeBAL binds the hash for the recommit
				// case (which the caller still seals); new-head aborts the
				// build entirely via commitWork's default error branch.
				return signalToErr(signal)
			}
		}
		if env.gasPool.Gas() < params.TxGas {
			log.Trace("Not enough gas for further transactions", "have", env.gasPool, "want", params.TxGas)
			break
		}

		tSpan := time.Now()
		tx := txSet.Peek()
		if tx == nil {
			break
		}

		txSize, decision, sizeErr := sizeLimiter.admit(tx)
		if decision == packStop {
			log.Trace("Block size budget exhausted, stopping tx packing",
				"txs", len(env.txs), "remaining", sizeLimiter.remaining())
			break
		}
		if decision == packSkipAccount {
			switch {
			case sizeErr != nil:
				log.Error("failed to encode pending transaction for size accounting",
					"hash", tx.Hash(), "err", sizeErr)
			case sizeLimiter.tooLargeForAnyBlock(txSize):
				log.Warn("transaction too large to ever fit a block, skipping",
					"hash", tx.Hash(), "size", txSize, "budget", sizeLimiter.budget)
			}
			txSet.Pop()
			continue
		}
		tExec := time.Now()
		dPick += tExec.Sub(tSpan)

		err := commitTx(tx)
		dCommit += time.Since(tExec)
		tShift := time.Now()
		switch {
		case err == nil:
			sizeLimiter.add(txSize)
			env.tcount++
			txSet.Shift() // Move to next tx from same account
		case errors.Is(err, internal.ErrGasLimitReached):
			popGas++
			txSet.Pop() // Skip this account entirely
		case errors.Is(err, internal.ErrNonceTooHigh):
			popNonceHigh++
			txSet.Pop() // Nonce gap, skip account
		case errors.Is(err, internal.ErrNonceTooLow):
			skipNonceLow++
			txSet.Shift() // Try next nonce from same account
		case errors.Is(err, internal.ErrFeeCapTooLow):
			// The account cannot pay this block's base fee. Skip the whole
			// account: its later transactions are gated behind this nonce, so
			// none of them can be included however much they bid.
			//
			// This is the common case, not an anomaly. A saturated chain drives
			// the base fee above a fixed-price load's cap by design (EIP-1559
			// settles at the gas target), and every transaction in the pool then
			// fails here. Shifting instead of popping walked the entire pool per
			// build, and logging each failure at Error put thousands of lines in
			// the log per block -- the empty block that produced them was three
			// times more expensive to build than a full one.
			priceOut++
			txSet.Pop()
		default:
			popOther++
			log.Error("miningCommitTx failed", "error", err)
			txSet.Shift()
		}
		dHeap += time.Since(tShift)
	}
	if tl != nil {
		tl.FillLoopDoneAt = time.Now() // S75: "fill loop done"
		tl.FillExecDur = dCommit       // S75: fillExec = ApplyTransaction time only
	}
	lookupWait := time.Duration(commitment.QMDBLookupWaitNanos() - lookupWait0)

	// One line per non-trivial build: where fillTx's time actually goes.
	// pick = Peek + size admit, commit = ApplyTransaction (the EVM work an
	// importer would also pay), heap = Shift/Pop re-sorting. The importer
	// executes the same transactions in ~5-6µs each; whatever pick+heap add on
	// top of commit is the builder's own overhead.
	if env.tcount > 1000 {
		log.Info("miner: fillTx breakdown", "pendingAccts", len(pending), "pendingRawAccts", pendingRawAccts, "pendingTxs", pendingTxs, "priceOut", priceOut,
			"pendingSnapshot", dPending, "trim", dTrim,
			"popGas", popGas, "popNonceHigh", popNonceHigh, "skipNonceLow", skipNonceLow, "staleTrimmed", staleTrimmed, "popOther", popOther, "lookupWait", lookupWait,
			"txs", env.tcount, "pick", dPick, "commit", dCommit, "heap", dHeap)
	}

	if priceOut > 0 {
		log.Info("miner: accounts priced out by the base fee",
			"accounts", priceOut, "of", len(pending), "packed", len(env.txs), "baseFee", header.BaseFee)
	}

	// Prune old bundles.
	if w.bundlePool != nil {
		w.bundlePool.PruneBundles(blockNumber)
	}

	// Bind the BAL hash even for an empty block or a block containing only bundle
	// transactions. Once the fork is active the header field is mandatory.
	return nil
}

// trimStalePending drops from pending every transaction whose nonce is
// below its sender's state nonce in reader (already mined) and returns how
// many it dropped. An account whose state cannot be read is left alone.
func trimStalePending(reader state.StateReader, pending map[types.Address][]*transaction.Transaction) int {
	trimmed := 0
	for addr, list := range pending {
		var nonce uint64
		if acc, rerr := reader.ReadAccountData(addr); rerr == nil && acc != nil {
			nonce = acc.Nonce
		} else if rerr != nil {
			continue // cannot tell; let execution decide as before
		}
		i := 0
		for i < len(list) && list[i].Nonce() < nonce {
			i++
		}
		if i == 0 {
			continue
		}
		trimmed += i
		if i == len(list) {
			delete(pending, addr)
		} else {
			pending[addr] = list[i:]
		}
	}
	return trimmed
}
