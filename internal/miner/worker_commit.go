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
// Applying transactions to the environment (commit) and pending-block snapshots.

package miner

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/streamverify"
	"github.com/n42blockchain/N42/internal/zkprover"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/witness"
)

func (w *worker) commit(env *environment, writer state.WriterWithChangeSets, ibs *state.IntraBlockState, start time.Time, needHeaders []*block.Header, tracingReader *witness.TracingReader, readLogRecorder *streamverify.ReadLogRecorder, speculative bool, specParent types.Hash, triggerAt time.Time, tPaceEnter time.Time, dPace time.Duration) error {
	if !w.isRunning() {
		return nil
	}

	// Phase timings (observability only). NOTE: the "miner: build phases" line
	// is emitted by commitWork BEFORE it calls commit(), so it does NOT cover
	// FinalizeAndAssemble — and that is where state root #1 is computed
	// (engine.Finalize → ibs.IntermediateRoot). These counters close that gap.
	tCommitStart := time.Now()
	var dWitness time.Duration

	envCopy := env.copy()
	dCopy := time.Since(tCommitStart)
	deferredExec := block.DeferredAt(envCopy.header.Time)
	if deferredExec {
		// The header carries the parent's executed result; this block's own
		// is recorded below and appears in the next header.
		rtx, terr := w.chain.DB().BeginRo(w.ctx)
		if terr != nil {
			return terr
		}
		pr, perr := w.laggedExecutedResult(rtx, envCopy.header)
		rtx.Rollback()
		if perr != nil {
			return fmt.Errorf("miner: deferred execution: %w", perr)
		}
		envCopy.header.Root, envCopy.header.ReceiptHash, envCopy.header.Bloom, envCopy.header.GasUsed = pr.Root, pr.ReceiptHash, pr.Bloom, pr.GasUsed
	}
	if len(envCopy.frameHashes) > 0 {
		envCopy.header.SetFrameTxRoot(block.FrameTxRoot(envCopy.frameHashes))
	}
	iblock, rewards, unpay, err := w.engine.FinalizeAndAssemble(w.chain, envCopy.header, ibs, envCopy.txs, nil, envCopy.receipts)
	if err != nil {
		return err
	}
	if len(envCopy.frameHashes) > 0 {
		body, ok := iblock.Body().(*block.Body)
		if !ok || iblock.TxHash() != block.FrameTxRoot(envCopy.frameHashes) {
			return errors.New("miner: frame block assembly lost the frame root")
		}
		body.FrameHashes = envCopy.frameHashes
		body.FrameData = envCopy.frameData
		// A chained build on this unwritten block must not take its frames
		// again; writing the block clears the mark.
		if bc, ok := w.chain.(*internal.BlockChain); ok {
			bc.MarkFramesInFlight(envCopy.frameHashes, envCopy.header.Number.Uint64())
		}
	}
	var exec *rawdb.ExecutedResult
	if deferredExec {
		root, ok := ibs.LastIntermediateRoot()
		if !ok {
			return errors.New("miner: deferred execution: the build computed no root")
		}
		r := internal.ExecutedResultFor(w.chainConfig, envCopy.header.Number.Uint64(), root, envCopy.receipts)
		exec = &r
	}
	dFinalize := time.Since(tCommitStart)
	// S75 (docs/QS_WIN2_RESIDUAL.md): "finalize done". The leader never times
	// state-root computation separately from FinalizeAndAssemble (unlike the
	// follower's "blockimport phases" root field) -- rootDone below is this
	// same instant, not an independent measurement; see the commit message.
	tFinalizeDoneAt := time.Now()
	tRootDoneAt := tFinalizeDoneAt

	// Mobile-verification packet: the recorder captured this block's read log
	// during the build. The packet is NOT built here: its header must be the
	// SEALED one (the BLS seal fills the reserved tail of Extra afterwards and
	// changes the block hash), so the job rides on the task and resultLoop
	// builds the packet once the sealed block arrives. Best-effort by design;
	// packet production must never fail a block.
	tPacket := time.Now()
	var pktJob *mobilePacketJob
	if w.mobilePacketSink != nil {
		pktJob = w.mobilePacketJobFor(envCopy, readLogRecorder)
	}
	dPacket := time.Since(tPacket)

	// Generate witness after FinalizeAndAssemble when JMT + TracingReader are active.
	if tracingReader != nil {
		tWitness := time.Now()
		if bc, ok := w.chain.(*internal.BlockChain); ok && bc.JMTCommitment() != nil {
			gen, gerr := witness.NewGenerator(bc.JMTCommitment())
			if gerr != nil {
				log.Warn("Failed to create witness generator", "err", gerr)
			}
			var bw *witness.BlockWitness
			var werr error
			if gen != nil {
				parentRoot := bc.CurrentBlock().StateRoot()
				bw, werr = gen.Generate(parentRoot, tracingReader, ibs.CodeHashes())
			}
			if werr != nil {
				log.Warn("Failed to generate block witness", "err", werr)
			} else {
				bc.StoreWitness(iblock.Hash(), bw)
				iblockNumber, numberErr := requireBlockNumber(iblock, "finalized block number unavailable")
				if numberErr != nil {
					log.Warn("Block witness generated without block number", "err", numberErr)
				} else {
					log.Debug("Block witness generated", "block", iblockNumber.Uint64(), "accounts", len(bw.AccountProofs), "storage", len(bw.StorageProofs))

					// Async submit to ZK prover if configured.
					if w.zkProverService != nil {
						parentHdr := bc.CurrentBlock().Header()
						go func(blkHash types.Hash, blkNum uint64, bwCopy *witness.BlockWitness, ph block.IHeader) {
							guestInput, err := zkprover.BuildGuestInput(w.chainConfig, iblock, ph, bwCopy)
							if err != nil {
								log.Warn("Failed to build guest input for ZK prover", "err", err)
								return
							}
							if err := w.zkProverService.SubmitBlock(blkHash, blkNum, guestInput); err != nil {
								log.Warn("Failed to submit block to ZK prover", "err", err)
							}
						}(iblock.Hash(), iblockNumber.Uint64(), bw, parentHdr)
					}
				}
			}
		}
		dWitness = time.Since(tWitness)
	}

	envHeaderNumber, err := requireHeaderNumber(envCopy.header, "mining header number unavailable")
	if err != nil {
		return err
	}
	// The Entire event re-marshals every transaction of the block — ~23k
	// encodings and a full snapshot copy on a saturated 480M block, ~100ms of
	// the leader's critical path. Its only consumers are on-demand RPC
	// subscriptions (minedBlock / aggregate-sign streams), which almost never
	// exist on a validator, so ask before building rather than after.
	if w.chainConfig.IsBeijing(envHeaderNumber.Uint64()) &&
		event.GlobalEvent.HasSubscribers(common.MinedEntireEvent{}) {
		txs := make([][]byte, len(envCopy.txs))
		for i, tx := range envCopy.txs {
			txs[i], err = tx.Marshal()
			if err != nil {
				return fmt.Errorf("failed to marshal transaction %d: %w", i, err)
			}
		}
		concreteHeader, ok := iblock.Header().(*block.Header)
		if !ok || concreteHeader == nil {
			return errors.New("invalid finalized block header type")
		}

		entire := state.Entire{
			Header:       concreteHeader,
			Transactions: txs,
			Snap:         ibs.Snap(),
			Proof:        types.Hash{},
		}
		cs := ibs.CodeHashes()
		hs := make(state.HashCodes, 0, len(cs))
		for k, v := range cs {
			hs = append(hs, &state.HashCode{Hash: k, Code: v})
		}
		sort.Sort(hs)

		event.GlobalEvent.Send(common.MinedEntireEvent{
			Entire: state.EntireCode{
				Codes:    hs,
				Headers:  needHeaders,
				Entire:   entire,
				Rewards:  rewards,
				CoinBase: envCopy.coinbase,
			},
		})
	}

	tSnap := time.Now()
	w.updateSnapshot(envCopy, rewards)
	dSnap := time.Since(tSnap)
	if len(envCopy.txs) > 1000 {
		log.Info("miner: assemble breakdown",
			"txs", len(envCopy.txs), "copy", dCopy, "finalize", dFinalize-dCopy,
			"packet", dPacket, "snapshot", dSnap,
			"other", time.Since(tCommitStart)-dFinalize-dPacket-dSnap)
	}

	commitRewardCount := 0
	if b := iblock.Body(); b != nil {
		commitRewardCount = len(b.Reward())
	}

	// Speculative build: park the finished task for the next view's trigger
	// instead of sealing it. Sealing signs and PROPOSES; that right arrives
	// only with TriggerBlockProduction, which will collect this via
	// takeSpecTask when the guessed parent is confirmed.
	// Snapshot the block's effects while the state is still ours alone: the
	// write path mutates the objects, and a chained build may read the
	// snapshot at any time after the seal.
	var post *state.PostState
	if internal.MinerAdoptAppends() {
		post = state.CapturePostState(ibs)
	}

	if speculative {
		var tParked time.Time
		if contentionDiagEnabled {
			tParked = time.Now()
		}
		tSealedAt := time.Now()
		w.specMu.Lock()
		w.specTask = &task{
			receipts: envCopy.receipts, block: iblock, createdAt: time.Now(), state: ibs, nopay: unpay, post: post, exec: exec, mobilePacket: pktJob,
			finalize: dFinalize, witness: dWitness, assemble: time.Since(tCommitStart),
			buildBeginAt: start, specParkedAt: tParked, triggerAt: triggerAt,
		}
		w.specParent = specParent
		w.specMu.Unlock()
		num := uint64(0)
		if n := iblock.Number64(); n != nil {
			num = n.Uint64()
		}
		log.Info("miner: speculative build parked", "number", num, "txs", envCopy.tcount, "parent", specParent.Hex()[:12], "tMs", time.Now().UnixMilli())
		// S75: "blockSealed" for a speculative-hit block is this park instant
		// -- the block is fully built and immutable from here, it is only the
		// network hand-off (OutputBroadcast, logged from hotstuff) that is
		// deferred until the guessed parent is confirmed.
		logMinerBlockTimeline(iblock, triggerAt, tFinalizeDoneAt, tRootDoneAt, tSealedAt, envCopy.tcount)
		return nil
	}

	var tTaskSent time.Time
	if contentionDiagEnabled {
		tTaskSent = time.Now()
	}
	select {
	case w.taskCh <- &task{
		receipts: envCopy.receipts, block: iblock, createdAt: time.Now(), state: ibs, nopay: unpay, post: post, exec: exec, mobilePacket: pktJob,
		finalize: dFinalize, witness: dWitness, assemble: time.Since(tCommitStart),
		buildBeginAt: start, triggerAt: triggerAt, paceEnterAt: tPaceEnter, paceDur: dPace, taskChSentAt: tTaskSent,
	}:
		blockNumber := uint64(0)
		if number := iblock.Number64(); number != nil {
			blockNumber = number.Uint64()
		}
		log.Debug("Commit new sealing work",
			"number", blockNumber,
			"sealhash", w.engine.SealHash(iblock.Header()),
			"txs", envCopy.tcount,
			"gas", iblock.GasUsed(),
			"elapsed", common.PrettyDuration(time.Since(start)),
			"headerTime", time.Unix(int64(iblock.Time()), 0).Format(time.RFC3339),
			"rewardCount", commitRewardCount,
		)
		// S75 (docs/QS_WIN2_RESIDUAL.md): "blockSealed" -- the block is handed
		// to the sealer/taskCh here; the network hand-off itself is logged
		// from hotstuff's onBlockReady ("proposal handed to network").
		logMinerBlockTimeline(iblock, triggerAt, tFinalizeDoneAt, tRootDoneAt, time.Now(), envCopy.tcount)
	case <-w.ctx.Done():
		return w.ctx.Err()
	}
	return nil
}

func copyReceipts(receipts []*block.Receipt) []*block.Receipt {
	result := make([]*block.Receipt, len(receipts))
	for i, r := range receipts {
		cpy := *r
		result[i] = &cpy
	}
	return result
}

func (w *worker) pendingBlockAndReceipts() (block.IBlock, block.Receipts) {
	w.snapshotMu.Lock()
	defer w.snapshotMu.Unlock()
	if w.snapshotBlock == nil && w.snapshotEnv != nil {
		w.snapshotBlock = block.NewBlockFromReceipt(
			w.snapshotEnv.header,
			w.snapshotEnv.txs,
			nil,
			w.snapshotEnv.receipts,
			w.snapshotRewards,
		)
	}
	return w.snapshotBlock, w.snapshotReceipts
}

// updateSnapshot stores the material for the pending block/receipts RPC
// snapshot. The block itself is assembled lazily on first read (see
// pendingBlockAndReceipts): NewBlockFromReceipt computes the bloom, the tx
// root and the receipt root over the whole block — pure waste on the leader's
// critical path when no one queries pending state.
//
// The caller passes the COPY commit() already made (envCopy), whose lifetime
// ends when commit returns and which nothing mutates afterwards, so the
// snapshot can hold its slices directly.
func (w *worker) updateSnapshot(env *environment, rewards []*block.Reward) {
	w.snapshotMu.Lock()
	defer w.snapshotMu.Unlock()

	w.snapshotEnv = env
	w.snapshotRewards = rewards
	w.snapshotBlock = nil // invalidate; rebuilt on demand
	w.snapshotReceipts = env.receipts
}

func signalToErr(signal int32) error {
	switch signal {
	case commitInterruptNewHead:
		return errBlockInterruptedByNewHead
	case commitInterruptResubmit:
		return errBlockInterruptedByRecommit
	case commitInterruptTimeout:
		return errBlockInterruptedByTimeout
	default:
		return fmt.Errorf("undefined signal %d", signal)
	}
}
