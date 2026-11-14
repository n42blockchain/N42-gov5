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
// Collecting parallel results and the apply/collect worker sizing.

package internal

import (
	"runtime"
	"strconv"
	"sync"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/envswitch"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// collectShardWorkers, from N42_COLLECT_SHARD, splits the bulk account
// pass's serial part across that many goroutines
// (state.ApplyAccountsSharded); 0, 1 or unset keeps ApplyAccounts.
var collectShardWorkers = parseCollectShard(envswitch.Raw("N42_COLLECT_SHARD", envswitch.Perf, "Goroutines that split the bulk account pass's serial part."))

func parseCollectShard(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	if n > 64 {
		n = 64
	}
	return n
}

// collectedRun is collectParallelResults' outcome.
type collectedRun struct {
	receipts  block.Receipts
	logs      []*block.Log
	usedGas   uint64
	feeTotals map[types.Address]*uint256.Int
	feeOrder  []types.Address
}

// collectParallelMinTxs is the block size from which the receipts and fee
// totals are assembled across goroutines.
const collectParallelMinTxs = 8192

// collectParallelResults assembles a run in which no transaction was
// dropped (skipped ones keep a skip receipt): receipts with cumulative gas,
// logs in order, the gas used, and the deferred fee totals per recipient
// in order of first appearance -- exactly what the serial loops produce.
// It works in two passes over contiguous chunks (sums, then placement after
// a prefix over the chunk sums). ok is false when the block is small or a
// kept transaction has no receipt; the caller then runs the serial loops.
func collectParallelResults(txs []*transaction.Transaction, txResults []parallelTxResult, skipped []bool, header *block.Header) (collectedRun, bool) {
	n := len(txs)
	if n < collectParallelMinTxs {
		return collectedRun{}, false
	}
	workers := n / collectParallelMinTxs
	if p := runtime.GOMAXPROCS(0); workers > p {
		workers = p
	}
	if workers > 16 {
		workers = 16
	}
	if workers < 2 {
		workers = 2
	}
	per := (n + workers - 1) / workers
	type chunk struct {
		gas    uint64
		nLogs  int
		noRcpt bool
		fees   []deferredFee // summed per recipient, first appearance order
		gasOff uint64
		logOff int
		lo, hi int
	}
	chunks := make([]chunk, workers)
	for w := range chunks {
		lo, hi := w*per, (w+1)*per
		if hi > n {
			hi = n
		}
		if lo > hi {
			lo = hi
		}
		chunks[w].lo, chunks[w].hi = lo, hi
	}
	isSkipped := func(i int) bool { return skipped != nil && skipped[i] }
	run := func(fn func(c *chunk)) {
		var wg sync.WaitGroup
		for w := range chunks {
			wg.Add(1)
			go func(c *chunk) {
				defer wg.Done()
				fn(c)
			}(&chunks[w])
		}
		wg.Wait()
	}
	run(func(c *chunk) {
		for i := c.lo; i < c.hi; i++ {
			tr := &txResults[i]
			for _, f := range tr.fees {
				k := 0
				for k < len(c.fees) && c.fees[k].recipient != f.recipient {
					k++
				}
				if k == len(c.fees) {
					c.fees = append(c.fees, deferredFee{recipient: f.recipient})
				}
				c.fees[k].amount.Add(&c.fees[k].amount, &f.amount)
			}
			if isSkipped(i) {
				continue
			}
			if tr.receipt == nil {
				c.noRcpt = true
				return
			}
			c.gas += tr.gasUsed
			c.nLogs += len(tr.logs)
		}
	})
	var out collectedRun
	nLogs := 0
	for w := range chunks {
		if chunks[w].noRcpt {
			return collectedRun{}, false
		}
		chunks[w].gasOff, chunks[w].logOff = out.usedGas, nLogs
		out.usedGas += chunks[w].gas
		nLogs += chunks[w].nLogs
	}
	out.receipts = make(block.Receipts, n)
	if nLogs > 0 {
		out.logs = make([]*block.Log, nLogs)
	}
	run(func(c *chunk) {
		cum, li := c.gasOff, c.logOff
		for i := c.lo; i < c.hi; i++ {
			if isSkipped(i) {
				out.receipts[i] = SkippedTxReceipt(txs[i], header, i, cum)
				continue
			}
			tr := &txResults[i]
			cum += tr.gasUsed
			tr.receipt.CumulativeGasUsed = cum
			out.receipts[i] = tr.receipt
			li += copy(out.logs[li:], tr.logs)
		}
	})
	out.feeTotals = make(map[types.Address]*uint256.Int, 2)
	for w := range chunks {
		for k := range chunks[w].fees {
			f := &chunks[w].fees[k]
			t, ok := out.feeTotals[f.recipient]
			if !ok {
				t = new(uint256.Int)
				out.feeTotals[f.recipient] = t
				out.feeOrder = append(out.feeOrder, f.recipient)
			}
			t.Add(t, &f.amount)
		}
	}
	return out, true
}

// ParallelWorkers reports the configured Block-STM worker count (for logs).
func ParallelWorkers() int { return parallelWorkers() }

// applyMVSWorkers is how many goroutines read a block's final versions in
// applyMVSToIBS: one for small blocks, up to 16 (and GOMAXPROCS).
func applyMVSWorkers(numTxs int) int {
	if state.FoldObjectSlabOn() {
		return applyMVSWorkersCut(numTxs)
	}
	w := numTxs / 4096
	if p := runtime.GOMAXPROCS(0); w > p {
		w = p
	}
	if w > 16 {
		w = 16
	}
	if w < 1 {
		w = 1
	}
	return w
}

// applyMVSWorkersCut is applyMVSWorkers under N42_AHEAD_COLLECT_CUT: one
// worker per collectCutPerWorker transactions, up to collectCutMaxWorkers
// (and GOMAXPROCS). The walk's parts are concatenated in shard order and
// the decode writes by index, so the fold is the same for any count.
func applyMVSWorkersCut(numTxs int) int {
	w := numTxs / collectCutPerWorker
	if p := runtime.GOMAXPROCS(0); w > p {
		w = p
	}
	if w > collectCutMaxWorkers {
		w = collectCutMaxWorkers
	}
	if w < 1 {
		w = 1
	}
	return w
}

var (
	collectCutPerWorker  = 2048
	collectCutMaxWorkers = 24
)
