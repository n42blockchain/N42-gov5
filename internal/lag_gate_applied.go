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

package internal

import (
	"os"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// The leader gate on the applied result (docs/QS_MULTI_MILLION_DESIGN.md
// P12).
//
// Under deferred depth k the leader proposes height N+1 once it has the
// executed result of N+1-k. The gate (LagReady) and the build read that
// result from the store, where the ordered writer puts it with the block's
// write -- ~140 ms after the import handed the block off (S103). Yet the
// result is final at the hand-off: the state root is computed, the receipts
// are final and their root and bloom are hashed on their own goroutine
// (receiptsResultFuture). Waiting for durability put the write into the
// proposal cycle: at depth 2 the leader proposed N+1 one write after its
// N-1 was handed off, so on the followers N+1 arrived after their import of
// N had settled (one import in three ran without its ahead execution) and
// half the leader views waited ~120 ms for the gate (S103 node0/node6).
//
// With N42_LAG_GATE_APPLIED (default on) the gate and the build take the
// result of the block whose write is in flight from memory, once its
// receipts part is hashed, and consensus is told then (the result hook)
// rather than at durability. The result is the one the write stores (same
// root, same receipts), and it is deterministic: a write that fails is
// executed again from the store, to the same result, so a header that
// carries it stays valid; followers check it against their own execution
// as before. Only the leader's proposal moves earlier -- imports, the
// commit path and every reader of the applied marker still see durable
// state only.

// lagGateApplied is N42_LAG_GATE_APPLIED (default on; "0", "false" or
// "off" waits for the durable write).
var lagGateApplied = parseLagPipeline(os.Getenv("N42_LAG_GATE_APPLIED"))

// SetLagGateAppliedForTest switches the gate on the applied result and
// returns the restore.
func SetLagGateAppliedForTest(on bool) func() {
	prev := lagGateApplied
	lagGateApplied = on
	return func() { lagGateApplied = prev }
}

// SetLagResultHook attaches the callback told when a handed-off block's
// executed result is known, before its write is durable (consensus: resume
// a leader view the lag gate parked on that block).
func (bc *BlockChain) SetLagResultHook(fn func(hash types.Hash)) {
	if fn == nil {
		bc.lagExec.resultHook.Store(nil)
		return
	}
	bc.lagExec.resultHook.Store(&fn)
}

// pendingExecutedResult returns the executed result of hash when it is the
// block whose write is in flight and the result is complete in memory (its
// receipts part hashed); it never waits.
func (bc *BlockChain) pendingExecutedResult(hash types.Hash) (rawdb.ExecutedResult, bool) {
	if !lagGateApplied {
		return rawdb.ExecutedResult{}, false
	}
	j := bc.lagWritePending(hash)
	if j == nil || !j.execRootOK || j.hdr == nil || bc.chainConfig == nil || !bc.chainConfig.IsDeferredExecution(j.hdr.Time) {
		return rawdb.ExecutedResult{}, false
	}
	f := j.future
	if !f.matches(hash, j.receipts) {
		return rawdb.ExecutedResult{}, false
	}
	select {
	case <-f.done:
	default:
		return rawdb.ExecutedResult{}, false
	}
	r := f.result
	r.Root = j.execRoot
	return r, true
}

// lagResultReadySoon tells the result hook once j's result is complete in
// memory, unless its write is durable first (the imported hook then tells
// consensus).
func (bc *BlockChain) lagResultReadySoon(j *lagWriteJob) {
	if !lagGateApplied || !j.execRootOK || !j.future.matches(j.hash, j.receipts) {
		return
	}
	fn := bc.lagExec.resultHook.Load()
	if fn == nil {
		return
	}
	go func() {
		select {
		case <-j.future.done:
		case <-j.done:
			return
		}
		if bc.lagWritePending(j.hash) == j {
			(*fn)(j.hash)
		}
	}()
}
