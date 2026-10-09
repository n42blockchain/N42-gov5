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
	"os"
	"strconv"
	"time"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
)

// EnvFrameBlockMaxFrames caps the frames in one block (default 1024).
const EnvFrameBlockMaxFrames = "N42_FRAME_BLOCK_MAX_FRAMES"

func frameBlockMaxFrames() int {
	if v, err := strconv.Atoi(os.Getenv(EnvFrameBlockMaxFrames)); err == nil && v > 0 {
		return v
	}
	return 1024
}

// framePick is the builder's choice of frames for one block.
type framePick struct {
	hashes []types.Hash
	raws   [][]byte
	gas    uint64 // sum of the frames' admission gas estimates
	txs    int
}

// pickFrames takes frames from the store in arrival order, oldest first,
// skipping those an unwritten ancestor already holds, and stops at the
// first frame whose gas estimate does not fit the budget or at maxFrames.
// Stopping rather than skipping keeps blocks in arrival order.
func pickFrames(st *store.Store, inFlight func(types.Hash) bool, budget uint64, maxFrames int) framePick {
	var p framePick
	st.Scan(func(h types.Hash, e store.Entry) bool {
		if inFlight != nil && inFlight(h) {
			return true
		}
		if len(p.hashes) >= maxFrames || p.gas+e.Gas > budget {
			return false
		}
		p.hashes = append(p.hashes, h)
		p.raws = append(p.raws, e.Raw)
		p.gas += e.Gas
		p.txs += e.Txs
		return true
	})
	return p
}

// fillFrames fills a frame-referenced block: whole frames from the frame
// store, executed under the skip rule. The parallel path is the leader's
// copy of the followers' Block-STM import; the serial path commits frame by
// frame and drops a frame that hits a failure outside the skip rule.
func (w *worker) fillFrames(env *environment, ibs *state.IntraBlockState, getHeader func(hash types.Hash, number uint64) *block.Header,
	commitTx func(*transaction.Transaction) error, parallelOK bool, tl *fillTimeline) error {
	bc, ok := w.chain.(*internal.BlockChain)
	if !ok || bc.FrameStore() == nil {
		return nil
	}
	header := env.header
	number := header.Number.Uint64()
	t0 := time.Now()
	pick := pickFrames(bc.FrameStore(), bc.FramesInFlight(), env.gasPool.Gas(), frameBlockMaxFrames())
	if len(pick.hashes) == 0 {
		return nil
	}
	dPick := time.Since(t0)
	tDecode := time.Now()
	per, dropped, err := frame.DecodeFramesEach(pick.raws, 0)
	if err != nil {
		// Admitted frames passed the layout check; this cannot happen
		// short of memory corruption, and is not worth a block.
		log.Error("miner: frame decode failed", "err", err)
		return nil
	}
	flat := make([]*transaction.Transaction, 0, pick.txs)
	for _, p := range per {
		flat = append(flat, p...)
	}
	dDecode := time.Since(tDecode)
	tExec := time.Now()
	finish := func(hashes []types.Hash, raws [][]byte, path string, skipped int) error {
		env.frameHashes = hashes
		env.frameData = raws
		bc.MarkFramesInFlight(hashes, number)
		if tl != nil {
			tl.FillLoopDoneAt = time.Now()
			tl.FillExecDur = time.Since(tExec)
			tl.PendingTxCount = len(flat)
		}
		log.Info("miner: frame fill", "n", number, "path", path, "frames", len(hashes), "picked", len(pick.hashes),
			"txs", len(env.txs), "skipped", skipped, "undecodable", dropped, "gasEst", pick.gas, "gasUsed", header.GasUsed,
			"storeFrames", bc.FrameStore().Len(), "pick", dPick, "decode", dDecode, "exec", time.Since(tExec))
		return nil
	}

	if parallelOK && parallelFillEnabled() && env.tcount == 0 && len(flat) > 4 {
		run, perr := bc.BuildParallelFrames(header, flat, ibs, internal.GetHashFn(header, getHeader))
		if perr == nil {
			if err := env.gasPool.SubGas(run.UsedGas); err != nil {
				return err
			}
			env.txs = append(env.txs, run.Included...)
			env.receipts = append(env.receipts, run.Receipts...)
			env.tcount += len(run.Included)
			header.GasUsed += run.UsedGas
			return finish(pick.hashes, pick.raws, "parallel", run.Skipped)
		}
		if !errors.Is(perr, internal.ErrParallelNotApplicable) && !errors.Is(perr, internal.ErrFrameTxNotSkippable) {
			return perr
		}
		log.Debug("miner: frame fill falls back to serial", "n", number, "reason", perr)
	}

	signer := transaction.MakeSignerWithTimestamp(w.chainConfig, header.Number.ToBig(), header.Time)
	var (
		hashes  []types.Hash
		raws    [][]byte
		skipped int
	)
	for fi, txs := range per {
		snap := ibs.Snapshot()
		gasSnap, usedSnap := env.gasPool.Gas(), header.GasUsed
		nTxs, nRcpt, tcount, skipSnap := len(env.txs), len(env.receipts), env.tcount, skipped
		ok := true
		for _, tx := range txs {
			err := commitTx(tx)
			if err == nil {
				env.tcount++
				continue
			}
			if internal.IsFrameSkippable(err, tx, signer) {
				env.txs = append(env.txs, tx)
				env.receipts = append(env.receipts, internal.SkippedTxReceipt(tx, header, env.tcount, header.GasUsed))
				env.tcount++
				skipped++
				continue
			}
			log.Warn("miner: frame dropped from block", "n", number, "frame", pick.hashes[fi], "tx", tx.Hash(), "err", err)
			ok = false
			break
		}
		if !ok {
			ibs.RevertToSnapshot(snap)
			env.gasPool = new(common.GasPool).AddGas(gasSnap)
			header.GasUsed = usedSnap
			env.txs, env.receipts, env.tcount, skipped = env.txs[:nTxs], env.receipts[:nRcpt], tcount, skipSnap
			continue
		}
		hashes = append(hashes, pick.hashes[fi])
		raws = append(raws, pick.raws[fi])
	}
	if len(hashes) == 0 {
		return nil
	}
	return finish(hashes, raws, "serial", skipped)
}
