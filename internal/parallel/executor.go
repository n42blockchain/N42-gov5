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

package parallel

import (
	"bytes"
	"os"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/log"
)

const (
	// MaxWaves is the safety limit on re-execution waves.
	// Block-STM converges in O(numTxs) waves worst-case.
	MaxWaves = 64
)

// TxExecuteFunc is called to execute a single transaction.
// It receives the transaction index and the read/write set to populate.
// Returns an error if execution fails (non-retryable).
type TxExecuteFunc func(txIndex int, rw *ReadWriteSet) error

// TxResult stores the execution result for a single transaction.
type TxResult struct {
	Err error // nil if successful
}

// Executor runs Block-STM wave-based parallel execution for a set of transactions.
//
// Algorithm:
//  1. Execute all transactions in parallel (initial wave).
//  2. Validate in order (tx0, tx1, ...). For each validation failure,
//     mark the failed tx and all later txs as needing re-execution.
//  3. Re-execute marked txs in parallel (next wave).
//  4. Repeat until all txs are validated.
//
// This is simpler than full Block-STM (no overlapping execute/validate phases)
// but still provides significant speedup for blocks with mostly independent txs.
type Executor struct {
	mvs     *MVS
	numTxs  int
	workers int

	// Per-transaction state (indexed by txIndex).
	status      []TxStatus
	incarnation []uint32
	rwSets      []*ReadWriteSet
	results     []TxResult

	// Execution function provided by caller.
	execFn TxExecuteFunc

	// workerSetup, when set, runs once per worker goroutine per wave and
	// returns a per-worker context handed to every execFn call that worker
	// makes, plus a teardown run when the worker exits. It exists so each
	// worker can own resources that cannot be shared across goroutines -- a
	// read transaction bound to its OS thread and a state reader over it
	// (3709ca6a: the workers used to share one MDBX cursor).
	workerSetup WorkerSetupFunc
	execCtxFn   TxExecuteWithCtxFunc

	// affinity, when set, pins every transaction with the same key to the
	// same worker, which executes its transactions in index order. A
	// sender's nonce chain then never conflicts with itself: each link reads
	// the previous link's write from the multi-version store, already there
	// because the same worker applied it. Round 35: without this, a block of
	// 4,000 senders x ~25 transactions each hit the 64-wave limit on every
	// block and fell back to sequential, 43 s for 163k.
	affinity func(txIndex int) uint64

	// Metrics.
	totalExecutions atomic.Int64
	totalAborts     atomic.Int64
	waves           int
	fellBack        bool
	traceLeft       int // N42_PARALLEL_TRACE: validation failures still to log this Run
}

// WorkerSetupFunc prepares one worker's private context. It is called on the
// worker's own goroutine, so anything it opens is used on the goroutine that
// opened it. teardown may be nil.
type WorkerSetupFunc func(workerID int) (ctx any, teardown func(), err error)

// TxExecuteWithCtxFunc is TxExecuteFunc with the worker context.
type TxExecuteWithCtxFunc func(ctx any, txIndex int, rw *ReadWriteSet) error

// NewExecutorWithWorkerSetup is NewExecutor for callers whose workers own
// resources: setup runs per worker goroutine, execFn receives that worker's
// context. A setup error fails every transaction that worker would have run,
// which surfaces as a block execution error rather than a silent fallback.
func NewExecutorWithWorkerSetup(numTxs int, workers int, setup WorkerSetupFunc, execFn TxExecuteWithCtxFunc) *Executor {
	e := NewExecutor(numTxs, workers, nil)
	e.workerSetup = setup
	e.execCtxFn = execFn
	return e
}

// SetAffinity pins transactions with equal keys to one worker, in index
// order (see the affinity field). Call before Run.
func (e *Executor) SetAffinity(key func(txIndex int) uint64) { e.affinity = key }

// NewExecutor creates a Block-STM executor for numTxs transactions.
// workers specifies the number of goroutines; 0 means runtime.NumCPU().
func NewExecutor(numTxs int, workers int, execFn TxExecuteFunc) *Executor {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if workers > numTxs {
		workers = numTxs
	}

	rwSets := make([]*ReadWriteSet, numTxs)
	for i := range rwSets {
		rwSets[i] = NewReadWriteSet(i)
	}

	return &Executor{
		mvs:         NewMVS(),
		numTxs:      numTxs,
		workers:     workers,
		status:      make([]TxStatus, numTxs),
		incarnation: make([]uint32, numTxs),
		rwSets:      rwSets,
		results:     make([]TxResult, numTxs),
		execFn:      execFn,
	}
}

// Run executes all transactions using wave-based Block-STM.
func (e *Executor) Run() []TxResult {
	if e.numTxs == 0 {
		return nil
	}

	// For very small blocks, sequential is more efficient.
	if e.numTxs <= 2 {
		e.runSequential()
		return e.results
	}

	if os.Getenv("N42_PARALLEL_TRACE") != "" {
		e.traceLeft = 12
	}
	for wave := 0; wave < MaxWaves; wave++ {
		e.waves = wave + 1
		// Collect txs that need (re-)execution.
		pending := e.collectPending()
		if len(pending) == 0 {
			break // all validated
		}
		if e.traceLeft >= 0 && os.Getenv("N42_PARALLEL_TRACE") != "" {
			log.Info("parallel trace: wave", "wave", wave, "pending", len(pending), "txs", e.numTxs, "first", pending[0], "last", pending[len(pending)-1])
		}

		// Execute pending txs in parallel.
		e.executeParallel(pending)

		// Validate in order. On first failure, mark it and all later txs as pending.
		allValid := e.validateInOrder()
		if allValid {
			break
		}
	}

	// Safety check: if not all validated after MaxWaves, fall back to sequential.
	if !e.allValidated() {
		log.Warn("Block-STM: wave limit reached, sequential fallback",
			"txs", e.numTxs,
			"waves", MaxWaves,
			"executions", e.totalExecutions.Load(),
			"aborts", e.totalAborts.Load(),
		)
		e.fellBack = true
		e.mvs = NewMVS()
		e.runSequential()
		return e.results
	}

	log.Debug("Block-STM execution completed",
		"txs", e.numTxs,
		"executions", e.totalExecutions.Load(),
		"aborts", e.totalAborts.Load(),
	)

	return e.results
}

// collectPending returns indices of txs that need (re-)execution.
func (e *Executor) collectPending() []int {
	// Only transactions the validator marked pending re-execute (with their
	// incarnation already advanced). An executed-but-unvalidated transaction
	// is provisional and gets re-VALIDATED next pass, not re-run: re-running
	// it here rewrote its value under an unchanged incarnation, which a
	// dependent that had recorded that incarnation could never detect.
	var pending []int
	for i := 0; i < e.numTxs; i++ {
		if e.status[i] == StatusPending {
			pending = append(pending, i)
		}
	}
	return pending
}

// executeParallel executes the given tx indices in parallel using the worker pool.
func (e *Executor) executeParallel(txIndices []int) {
	var wg sync.WaitGroup
	// With an affinity key, each worker gets its own in-order queue of the
	// transactions that hash to it; without one, a shared channel.
	var queues [][]int
	work := make(chan int, len(txIndices))
	if e.affinity != nil {
		queues = make([][]int, e.workers)
		for _, idx := range txIndices { // txIndices is ascending
			w := int(e.affinity(idx) % uint64(e.workers))
			queues[w] = append(queues[w], idx)
		}
	}

	// Start workers.
	for i := 0; i < e.workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					buf := make([]byte, 4096)
					n := runtime.Stack(buf, false)
					log.Error("panic in parallel executor worker, recovered", "panic", r, "stack", string(buf[:n]))
				}
			}()
			var ctx any
			var setupErr error
			if e.workerSetup != nil {
				var teardown func()
				ctx, teardown, setupErr = e.workerSetup(workerID)
				if teardown != nil {
					defer teardown()
				}
			}
			run := func(txIndex int) {
				if setupErr != nil {
					e.results[txIndex] = TxResult{Err: setupErr}
					e.status[txIndex] = StatusExecuted
					return
				}
				e.executeSingle(ctx, txIndex)
			}
			if queues != nil {
				for _, txIndex := range queues[workerID] {
					run(txIndex)
				}
				return
			}
			for txIndex := range work {
				run(txIndex)
			}
		}(i)
	}

	// Feed work (channel mode only).
	if queues == nil {
		for _, idx := range txIndices {
			work <- idx
		}
	}
	close(work)

	wg.Wait()
}

// executeSingle executes a single transaction.
func (e *Executor) executeSingle(ctx any, txIndex int) {
	e.totalExecutions.Add(1)

	// Allocate a fresh ReadWriteSet.
	rw := NewReadWriteSet(txIndex)

	// Clear the previous incarnation's writes that this one may not repeat.
	// Only that incarnation's keys: DeleteAll walks every entry in the store
	// and was 95% of a follower's CPU at 54k transactions (round 35e).
	if prev := e.rwSets[txIndex]; prev != nil {
		for _, wd := range prev.Writes {
			e.mvs.Delete(wd.Key, txIndex)
		}
	}

	// Execute the transaction.
	err := e.exec(ctx, txIndex, rw)

	e.results[txIndex] = TxResult{Err: err}
	e.rwSets[txIndex] = rw

	// Apply writes to MVS with incarnation tag.
	inc := e.incarnation[txIndex]
	for _, wd := range rw.Writes {
		e.mvs.Write(wd.Key, txIndex, inc, wd.Value)
	}

	e.status[txIndex] = StatusExecuted
}

// validateInOrder validates all executed txs in order.
// On first failure, marks the failed tx and all later txs as pending.
// Returns true if all txs are validated.
func (e *Executor) validateInOrder() bool {
	// A pass validates every executed transaction in order. A failure at i
	// re-executes i alone (incarnation++); every transaction after i is
	// provisional from then on -- it may have read i's old write -- so at the
	// end of the pass everything after the FIRST failure is demoted to
	// Executed and re-validated next pass, when i's new write is in the
	// store. Transactions that fail later in the same pass are marked pending
	// too, so independent conflicts are all re-executed in one wave rather
	// than one per wave. Re-executing every later transaction on the first
	// failure (the old rule) cost a wave per conflict; round 35 hit the
	// 64-wave limit on every block with it.
	firstFail := -1
	for i := 0; i < e.numTxs; i++ {
		switch e.status[i] {
		case StatusValidated:
			continue // settled in an earlier pass, and nothing before it moved
		case StatusPending:
			if firstFail < 0 {
				firstFail = i
			}
			continue
		}
		if Validate(e.mvs, e.rwSets[i]) {
			e.status[i] = StatusValidated
			continue
		}
		if e.traceLeft > 0 {
			e.traceLeft--
			e.traceFailure(i)
		}
		e.totalAborts.Add(1)
		e.status[i] = StatusPending
		e.incarnation[i]++
		if firstFail < 0 {
			firstFail = i
		}
	}
	if firstFail < 0 {
		return true
	}
	for j := firstFail + 1; j < e.numTxs; j++ {
		if e.status[j] == StatusValidated {
			e.status[j] = StatusExecuted
		}
	}
	return false
}

// traceFailure logs why transaction i failed validation (N42_PARALLEL_TRACE).
func (e *Executor) traceFailure(i int) {
	rw := e.rwSets[i]
	for _, rd := range rw.Reads {
		cur, wtx, winc, found := e.mvs.Read(rd.Key, i)
		ok := false
		switch {
		case rd.FromBase && !found:
			ok = true
		case !rd.FromBase && found && wtx == rd.WriterTx && winc == rd.WriterIncarnation:
			ok = true
		case found && rd.HasValue && bytes.Equal(cur, rd.Value):
			ok = true
		}
		if ok {
			continue
		}
		log.Info("parallel trace: stale read", "tx", i, "inc", e.incarnation[i], "addr", rd.Key.Address.Hex(), "field", rd.Key.Field, "slot", rd.Key.Slot.Hex()[:10],
			"fromBase", rd.FromBase, "readWriter", rd.WriterTx, "readInc", rd.WriterIncarnation, "hasValue", rd.HasValue, "readLen", len(rd.Value),
			"nowFound", found, "nowWriter", wtx, "nowInc", winc, "nowLen", len(cur), "reads", len(rw.Reads), "writes", len(rw.Writes))
		return
	}
	log.Info("parallel trace: failed without a stale read", "tx", i, "reads", len(rw.Reads))
}

// Waves is the number of execute+validate passes the last Run took.
func (e *Executor) Waves() int { return e.waves }

// FellBack reports whether the last Run gave up on Block-STM and executed
// the block sequentially.
func (e *Executor) FellBack() bool { return e.fellBack }

// allValidated returns true if all transactions are validated.
func (e *Executor) allValidated() bool {
	for i := 0; i < e.numTxs; i++ {
		if e.status[i] != StatusValidated {
			return false
		}
	}
	return true
}

// exec dispatches to whichever execution function the executor was built with.
func (e *Executor) exec(ctx any, txIndex int, rw *ReadWriteSet) error {
	if e.execCtxFn != nil {
		return e.execCtxFn(ctx, txIndex, rw)
	}
	return e.execFn(txIndex, rw)
}

// runSequential falls back to sequential execution.
func (e *Executor) runSequential() {
	var ctx any
	if e.workerSetup != nil {
		c, teardown, err := e.workerSetup(0)
		if err != nil {
			for i := 0; i < e.numTxs; i++ {
				e.results[i] = TxResult{Err: err}
			}
			return
		}
		if teardown != nil {
			defer teardown()
		}
		ctx = c
	}
	for i := 0; i < e.numTxs; i++ {
		rw := e.rwSets[i]
		rw.Clear()

		err := e.exec(ctx, i, rw)
		e.results[i] = TxResult{Err: err}

		for _, wd := range rw.Writes {
			e.mvs.Write(wd.Key, i, 0, wd.Value)
		}
	}
}

// MVS returns the multi-version store (for testing/debugging).
func (e *Executor) MVS() *MVS {
	return e.mvs
}

// Stats returns execution statistics.
func (e *Executor) Stats() (executions, aborts int64) {
	return e.totalExecutions.Load(), e.totalAborts.Load()
}
