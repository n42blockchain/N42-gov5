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
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/envswitch"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
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
var lagGateApplied = envswitch.DefaultOn("N42_LAG_GATE_APPLIED", envswitch.Perf, "Default on: the lag gate waits for the applied state.")

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
// receipts part hashed), or a light E=1 node's fetched result whose block
// is not stored yet (e1EarlyResult); it never waits.
func (bc *BlockChain) pendingExecutedResult(hash types.Hash) (rawdb.ExecutedResult, bool) {
	if r, ok := bc.e1EarlyResult(hash); ok {
		return r, true
	}
	if !lagGateApplied {
		return rawdb.ExecutedResult{}, false
	}
	if r, ok := bc.inflightExecutedResult(hash); ok {
		return r, true
	}
	return bc.earlyExecutedResult(hash)
}

// inflightExecutedResult is pendingExecutedResult whatever
// N42_LAG_GATE_APPLIED says (the E=1 executor serves it, e1.go).
func (bc *BlockChain) inflightExecutedResult(hash types.Hash) (rawdb.ExecutedResult, bool) {
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

// N42_LAG_RESULT_EARLY (r69a). The gate on the applied result above takes a
// block's result from the write job, which exists only once the import's
// hand-off waited for the previous block's write (E1N26 run 34: handWait
// p50 5 ms, p90 26 ms): at depth 2 the leader builds N+2 only then, and the
// followers' child N+1 arrives that much closer to their hook of N. Yet the
// result is final as soon as the import's execution and validation passed:
// the state root is computed (ibs.LastIntermediateRoot, what the write
// stores) and the receipts part is hashed from the final receipts
// (receiptsResultFuture), exactly what the job then serves. With the switch
// on, the lag import publishes the result right after validation; the gate,
// the build and the result hook see it from then on, and the job takes over
// at the hand-off (the early result is withdrawn after it, so there is no
// gap). What may still fail after the publication -- the previous block's
// write (the tree is reloaded, both blocks are queued again), this block's
// hand-off or write -- leaves the result valid: the block executes again on
// the same parent state to the same result, which is the argument the gate
// on the applied result already rests on (a write in flight may fail too).
// Nothing durable is read through it: the store, the applied marker, the
// commit path and the import of the child still see durable or applied
// state only, as before. Only the leader's gate and build (and the result
// hook that resumes them) move earlier.

// lagResultEarlyOn is N42_LAG_RESULT_EARLY (default off; "1", "true" or
// "on" enables). It needs N42_LAG_GATE_APPLIED (default on).
var lagResultEarlyOn = envswitch.OptIn("N42_LAG_RESULT_EARLY", envswitch.Perf, "Opt-in: publish the lag result early.")

// SetLagResultEarlyForTest switches N42_LAG_RESULT_EARLY and returns the
// restore.
func SetLagResultEarlyForTest(on bool) func() {
	prev := lagResultEarlyOn
	lagResultEarlyOn = on
	return func() { lagResultEarlyOn = prev }
}

// lagResultsEarly counts results published before their hand-off, and
// lagResultsEarlyServed the gate or build reads they answered (since start).
var lagResultsEarly, lagResultsEarlyServed atomic.Uint64

// lagEarlyResultForTest, when set, is told each early result the moment it
// is complete (tests: compare it with the stored one).
var lagEarlyResultForTest func(bc *BlockChain, hash types.Hash, r rawdb.ExecutedResult)

// lagEarlyResult is one block's executed result published before its
// hand-off.
type lagEarlyResult struct {
	hash   types.Hash
	root   types.Hash
	future *receiptsResultFuture
	// gone closes once the result is withdrawn.
	gone chan struct{}
	once sync.Once
}

// result returns the complete result, or false while its receipts part is
// still hashed.
func (e *lagEarlyResult) result() (rawdb.ExecutedResult, bool) {
	select {
	case <-e.future.done:
	default:
		return rawdb.ExecutedResult{}, false
	}
	r := e.future.result
	r.Root = e.root
	return r, true
}

// lagPublishEarlyResult publishes blk's executed result (the import's
// execution and validation passed, its hand-off not begun) and tells the
// result hook once it is complete. nil when it publishes nothing: the
// switch or the applied gate off, a block not under deferred execution, no
// computed root, or no receipts part for these receipts. The caller
// withdraws it (lagWithdrawEarlyResult) once the hand-off returned.
func (bc *BlockChain) lagPublishEarlyResult(blk *block.Block, receipts []*block.Receipt, ibs *state.IntraBlockState) *lagEarlyResult {
	if !lagResultEarlyOn || !lagGateApplied || blk == nil || ibs == nil || bc.chainConfig == nil {
		return nil
	}
	hdr, ok := blk.Header().(*block.Header)
	if !ok || !bc.chainConfig.IsDeferredExecution(hdr.Time) {
		return nil
	}
	hash := blk.Hash()
	f := bc.receiptsFuture
	if !f.matches(hash, receipts) {
		return nil
	}
	root, ok := ibs.LastIntermediateRoot()
	if !ok {
		return nil
	}
	e := &lagEarlyResult{hash: hash, root: root, future: f, gone: make(chan struct{})}
	bc.lagWrite.early.Store(e)
	lagResultsEarly.Add(1)
	fn := bc.lagExec.resultHook.Load()
	test := lagEarlyResultForTest
	if fn == nil && test == nil {
		return e
	}
	go func() {
		select {
		case <-f.done:
		case <-e.gone:
			return
		}
		if test != nil {
			if r, ok := e.result(); ok {
				test(bc, hash, r)
			}
		}
		if fn != nil && bc.lagWrite.early.Load() == e {
			(*fn)(hash)
		}
	}()
	return e
}

// lagWithdrawEarlyResult withdraws e (the hand-off returned: its job, if
// any, serves the result from now on).
func (bc *BlockChain) lagWithdrawEarlyResult(e *lagEarlyResult) {
	if e == nil {
		return
	}
	bc.lagWrite.early.CompareAndSwap(e, nil)
	e.once.Do(func() { close(e.gone) })
}

// earlyExecutedResult returns hash's early result when it is published and
// complete; it never waits.
func (bc *BlockChain) earlyExecutedResult(hash types.Hash) (rawdb.ExecutedResult, bool) {
	if !lagResultEarlyOn {
		return rawdb.ExecutedResult{}, false
	}
	e := bc.lagWrite.early.Load()
	if e == nil || e.hash != hash {
		return rawdb.ExecutedResult{}, false
	}
	r, ok := e.result()
	if ok {
		lagResultsEarlyServed.Add(1)
	}
	return r, ok
}
