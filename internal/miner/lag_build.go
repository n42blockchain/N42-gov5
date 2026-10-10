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

package miner

import (
	"errors"
	"fmt"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// Leader build under deferred depth k > 1 (docs/QS_MULTI_MILLION_DESIGN.md
// P2a item c). The leader fills frames and proposes without executing them:
// the header carries this node's executed result of block N-k (the lag gate
// in the consensus service waits for it), the body the frame hashes picked
// in arrival order, and the block is executed later by the chain's lag
// executor like any follower's. Frames are atomic and the skip rule settles
// stale or unfunded transactions at execution on every node alike, so the
// fill does not pre-execute: the per-transaction pre-check of section 5.2
// would only decide which whole frames to take, and taking them in arrival
// order is what keeps each sender's nonces contiguous. The pool's own
// transactions (local frames) are trimmed against the latest executed state
// and against the local frames still in flight in unexecuted blocks.

// frameAssembler builds a frame block without execution (the HotStuff
// adapter).
type frameAssembler interface {
	AssembleWithoutExecution(chain consensus.ChainHeaderReader, header *block.Header, frameHashes []types.Hash, frameData [][]byte) (block.IBlock, error)
}

// availabilityNotifier tells the engine this node holds its own unexecuted
// block (the leader's Round-2 gate under vote on availability).
type availabilityNotifier interface {
	NotifyBlockAvailable(hash, parent types.Hash)
}

// lagBuildEnabled reports whether this worker builds without executing:
// deferred depth above 1, frame blocks on, a frame store attached and an
// engine that can assemble a frame block.
func (w *worker) lagBuildEnabled() bool {
	if !block.LagMode() || !internal.FrameBlocksEnabled() {
		return false
	}
	bc, ok := w.chain.(*internal.BlockChain)
	if !ok || bc.FrameStore() == nil {
		return false
	}
	_, ok = w.engine.(frameAssembler)
	return ok
}

// ownSealedHeader resolves a header this node sealed and has not written.
func (w *worker) ownSealedHeader(hash types.Hash) *block.Header {
	if b := w.ownSealed(hash); b != nil {
		h, _ := b.Header().(*block.Header)
		return h
	}
	return nil
}

// laggedExecutedResult is the executed result a header built on
// header.ParentHash must carry at the depth that applies to it: the
// parent's at depth 1 (parentExecutedResult), else the block the lag
// reports, from this node's own sealed results or the store.
func (w *worker) laggedExecutedResult(tx kv.Getter, header *block.Header) (rawdb.ExecutedResult, error) {
	if block.DeferredDepthAt(header.Number.Uint64()) <= 1 {
		return w.parentExecutedResult(tx, header)
	}
	bc, ok := w.chain.(*internal.BlockChain)
	if !ok {
		return w.parentExecutedResult(tx, header)
	}
	target, err := bc.LaggedTarget(tx, header, w.ownSealedHeader)
	if err != nil {
		return rawdb.ExecutedResult{}, err
	}
	w.mu.RLock()
	r, ok := w.sealedExec[target.Hash()]
	w.mu.RUnlock()
	if ok {
		return r, nil
	}
	return bc.ExecutedOrSeededResult(tx, target)
}

// commitWorkLagged builds, seals and proposes a frame block on parentHash
// without executing it.
func (w *worker) commitWorkLagged(parentHash types.Hash, timestamp int64, triggerAt time.Time) error {
	start := time.Now()
	bc := w.chain.(*internal.BlockChain)
	w.mu.RLock()
	coinbase := w.coinbase
	w.mu.RUnlock()
	if w.isRunning() && coinbase == (types.Address{}) {
		return errors.New("coinbase is empty")
	}
	var parent *block.Header
	if parentHash == (types.Hash{}) {
		if cur := w.chain.CurrentBlock(); cur != nil {
			parent, _ = cur.Header().(*block.Header)
		}
	} else if parent = w.ownSealedHeader(parentHash); parent == nil {
		if h, _ := bc.GetHeaderByHash(parentHash); h != nil {
			parent, _ = h.(*block.Header)
		}
	}
	if parent == nil || parent.Number == nil {
		return fmt.Errorf("lag build: parent %x not known", parentHash[:8])
	}
	header := &block.Header{
		ParentHash: parent.Hash(),
		Coinbase:   coinbase,
		Number:     new(uint256.Int).AddUint64(parent.Number, 1),
		GasLimit:   CalcGasLimit(parent.GasLimit, w.minerConf.GasCeil),
		Time:       uint64(timestamp),
		Difficulty: uint256.NewInt(0),
		BaseFee:    uint256.NewInt(0),
	}
	if parent.Time >= header.Time {
		header.Time = parent.Time + 1
	}
	number := header.Number.Uint64()
	if w.chainConfig.IsLondon(number) {
		header.BaseFee, _ = uint256.FromBig(misc.CalcBaseFee(w.chainConfig, parent))
		if !w.chainConfig.IsLondon(parent.Number.Uint64()) {
			header.GasLimit = CalcGasLimit(parent.GasLimit*params.ElasticityMultiplier, w.minerConf.GasCeil)
		}
	}
	if err := w.engine.Prepare(w.chain, header); err != nil {
		return fmt.Errorf("lag build: prepare: %w", err)
	}
	if err := w.paceBlock(number); err != nil {
		return err
	}
	tFill := time.Now()
	tx, err := w.chain.DB().BeginRo(w.ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := w.laggedExecutedResult(tx, header)
	if err != nil {
		// The consensus lag gate admits the build only once the block the
		// header reports is executed here; this is a race with a reorg.
		return fmt.Errorf("lag build: deferred execution: %w", err)
	}
	header.Root, header.ReceiptHash, header.Bloom, header.GasUsed = res.Root, res.ReceiptHash, res.Bloom, res.GasUsed
	if w.mobileAnchorRoot != nil && w.chainConfig.IsMobileAnchor(header.Time) {
		header.MobileRegistryRoot = w.mobileAnchorRoot()
	}

	env := &environment{coinbase: coinbase, header: header}
	budget := fillGasBudget(header.GasLimit)
	st := bc.FrameStore()
	inFlight := bc.FramesInFlight()
	maxFrames := frameBlockMaxFrames()
	// Pool transactions against the latest executed state; the ones a local
	// frame of an unexecuted block holds are left out.
	ibs := state.New(state.NewPlainStateReader(tx))
	local, own := w.fillLocalFrames(env, ibs, st, inFlight, budget, maxFrames)
	gw := pickFrames(st, inFlight, own, budget-local.gas, maxFrames-len(local.hashes))
	hashes := append(local.hashes, gw.hashes...)
	raws := append(local.raws, gw.raws...)
	header.SetFrameTxRoot(block.FrameTxRoot(hashes))
	dFill := time.Since(tFill)

	asm := w.engine.(frameAssembler)
	iblock, err := asm.AssembleWithoutExecution(w.chain, header, hashes, raws)
	if err != nil {
		return err
	}
	bc.MarkFramesInFlight(hashes, number)
	w.markLocalFramesInFlight(local.hashes, number)
	log.Info("miner: lag build", "n", number, "parent", parent.Hash().Hex()[:12], "frames", len(hashes),
		"localFrames", len(local.hashes), "txs", local.txs+gw.txs, "gasEst", local.gas+gw.gas,
		"reports", number-uint64(block.DeferredDepthAt(number)), "fill", dFill, "total", time.Since(start),
		"storeFrames", st.Len(), "tMs", time.Now().UnixMilli())

	select {
	case w.taskCh <- &task{block: iblock, createdAt: time.Now(), lagged: true, buildBeginAt: start, triggerAt: triggerAt}:
	case <-w.ctx.Done():
		return w.ctx.Err()
	}
	return nil
}

// finishLaggedSeal hands a sealed block built without execution to the
// network and to consensus, and to the chain's lag executor to execute.
func (w *worker) finishLaggedSeal(blk block.IBlock, sealhash types.Hash, task *task) {
	bc, ok := w.chain.(*internal.BlockChain)
	concrete, ok2 := blk.(*block.Block)
	if !ok || !ok2 {
		return
	}
	bc.RememberAvailable(concrete)
	tPush := time.Now()
	if err := w.chain.SealedBlock(blk); err != nil {
		log.Warn("miner: lag block push failed", "err", err)
	}
	dPush := time.Since(tPush)
	if an, ok := w.engine.(availabilityNotifier); ok {
		an.NotifyBlockAvailable(blk.Hash(), blk.ParentHash())
	}
	if bsn, ok := w.engine.(blockSealNotifier); ok {
		bsn.NotifyBlockSealed(blk.Hash(), blk.TxHash())
	}
	bc.EnqueueLagImport(concrete)
	w.mu.Lock()
	delete(w.pendingTasks, sealhash)
	w.mu.Unlock()
	log.Info("miner: lag block sealed and proposed", "n", blk.Number64().Uint64(), "hash", blk.Hash().Hex()[:12],
		"push", dPush, "total", time.Since(task.createdAt), "tMs", time.Now().UnixMilli())
}

// markLocalFramesInFlight records the pool transactions of local frames a
// block at number took, so later builds leave them out until that block is
// committed (lag mode: the pool only drops them on the head move after
// execution).
func (w *worker) markLocalFramesInFlight(frames []types.Hash, number uint64) {
	if len(frames) == 0 {
		return
	}
	lf := &w.localFrames
	lf.mu.Lock()
	defer lf.mu.Unlock()
	if lf.inFlightTxs == nil {
		lf.inFlightTxs = make(map[types.Hash]uint64)
	}
	for _, f := range frames {
		for _, h := range lf.txsOf[f] {
			lf.inFlightTxs[h] = number
		}
	}
}

// dropInFlightPending removes from pending the leading transactions of each
// account that a local frame of an unexecuted block holds, and forgets the
// entries at or below the committed height (committed: the pool drops them
// itself; orphaned: their transactions are due again).
func (w *worker) dropInFlightPending(pending map[types.Address][]*transaction.Transaction, committed uint64) int {
	lf := &w.localFrames
	lf.mu.Lock()
	defer lf.mu.Unlock()
	for h, n := range lf.inFlightTxs {
		if n <= committed {
			delete(lf.inFlightTxs, h)
		}
	}
	if len(lf.inFlightTxs) == 0 {
		return 0
	}
	dropped := 0
	for addr, txs := range pending {
		i := 0
		for i < len(txs) {
			if _, ok := lf.inFlightTxs[txs[i].Hash()]; !ok {
				break
			}
			i++
		}
		if i == 0 {
			continue
		}
		dropped += i
		if i == len(txs) {
			delete(pending, addr)
		} else {
			pending[addr] = txs[i:]
		}
	}
	return dropped
}
