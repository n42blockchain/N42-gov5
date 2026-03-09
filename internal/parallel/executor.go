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

	// Metrics.
	totalExecutions atomic.Int64
	totalAborts     atomic.Int64
}

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

	for wave := 0; wave < MaxWaves; wave++ {
		// Collect txs that need (re-)execution.
		pending := e.collectPending()
		if len(pending) == 0 {
			break // all validated
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
	var pending []int
	for i := 0; i < e.numTxs; i++ {
		if e.status[i] != StatusValidated {
			pending = append(pending, i)
		}
	}
	return pending
}

// executeParallel executes the given tx indices in parallel using the worker pool.
func (e *Executor) executeParallel(txIndices []int) {
	var wg sync.WaitGroup
	work := make(chan int, len(txIndices))

	// Start workers.
	for i := 0; i < e.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					buf := make([]byte, 4096)
					n := runtime.Stack(buf, false)
					log.Error("panic in parallel executor worker, recovered", "panic", r, "stack", string(buf[:n]))
				}
			}()
			for txIndex := range work {
				e.executeSingle(txIndex)
			}
		}()
	}

	// Feed work.
	for _, idx := range txIndices {
		work <- idx
	}
	close(work)

	wg.Wait()
}

// executeSingle executes a single transaction.
func (e *Executor) executeSingle(txIndex int) {
	e.totalExecutions.Add(1)

	// Allocate a fresh ReadWriteSet.
	rw := NewReadWriteSet(txIndex)

	// Clear old MVS entries for this tx.
	e.mvs.DeleteAll(txIndex)

	// Execute the transaction.
	err := e.execFn(txIndex, rw)

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
	for i := 0; i < e.numTxs; i++ {
		if e.status[i] == StatusValidated {
			continue // already validated in a previous wave
		}

		rw := e.rwSets[i]
		if Validate(e.mvs, rw) {
			e.status[i] = StatusValidated
		} else {
			// This tx's read set is stale. Mark it and all later txs
			// as pending for re-execution.
			e.totalAborts.Add(1)
			for j := i; j < e.numTxs; j++ {
				if e.status[j] != StatusValidated {
					e.status[j] = StatusPending
					e.incarnation[j]++
				}
			}
			return false
		}
	}
	return true
}

// allValidated returns true if all transactions are validated.
func (e *Executor) allValidated() bool {
	for i := 0; i < e.numTxs; i++ {
		if e.status[i] != StatusValidated {
			return false
		}
	}
	return true
}

// runSequential falls back to sequential execution.
func (e *Executor) runSequential() {
	for i := 0; i < e.numTxs; i++ {
		rw := e.rwSets[i]
		rw.Clear()

		err := e.execFn(i, rw)
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
