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
	// MaxValidationRounds limits the number of validation+re-execution cycles
	// for the DAG executor. Since the DAG already captures most dependencies,
	// very few rounds should be needed (typically 0-1 re-execution rounds).
	MaxValidationRounds = 16
)

// DAGExecutor enhances Block-STM with dependency-aware scheduling.
//
// Instead of executing all transactions speculatively and relying purely on
// validation to detect conflicts, DAGExecutor pre-analyzes transaction access
// lists (EIP-2930) to build a dependency graph. It then schedules execution
// in topological batches: transactions in the same batch have no known
// dependencies and can run in parallel safely.
//
// For transactions without access lists, it falls back to conservative
// sequential ordering. Block-STM validation is still applied as a safety net
// to catch any dependencies not captured by access lists (e.g., dynamic
// storage accesses determined at runtime).
//
// Compared to pure Block-STM:
//   - Independent transactions: similar performance (DAG construction is cheap)
//   - Hot-spot scenarios: significantly fewer wasted re-executions
//   - Fully conflicting: executes sequentially with minimal overhead
type DAGExecutor struct {
	numTxs  int
	workers int
	execFn  TxExecuteFunc
	dag     *TxDAG
	mvs     *MVS

	// Per-transaction state.
	status      []TxStatus
	incarnation []uint32
	rwSets      []*ReadWriteSet
	results     []TxResult

	// Metrics.
	totalExecutions atomic.Int64
	totalAborts     atomic.Int64
}

// NewDAGExecutor creates a DAG-aware executor.
// It first builds the dependency graph from txInfos, then prepares for execution.
// workers specifies parallelism; 0 means runtime.NumCPU().
func NewDAGExecutor(numTxs int, workers int, txInfos []TxAccessInfo, execFn TxExecuteFunc) *DAGExecutor {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if workers > numTxs && numTxs > 0 {
		workers = numTxs
	}

	dag := BuildDAG(txInfos)

	rwSets := make([]*ReadWriteSet, numTxs)
	for i := range rwSets {
		rwSets[i] = NewReadWriteSet(i)
	}

	return &DAGExecutor{
		numTxs:      numTxs,
		workers:     workers,
		execFn:      execFn,
		dag:         dag,
		mvs:         NewMVS(),
		status:      make([]TxStatus, numTxs),
		incarnation: make([]uint32, numTxs),
		rwSets:      rwSets,
		results:     make([]TxResult, numTxs),
	}
}

// Run executes all transactions respecting the dependency graph.
//
// Algorithm:
//  1. Compute topological batches from the DAG.
//  2. Execute each batch in parallel (transactions within a batch are independent).
//  3. After all batches complete, validate all transactions in order using Block-STM
//     validation to catch any runtime dependencies missed by static analysis.
//  4. If validation fails, re-execute the failed transaction and all subsequent
//     unvalidated transactions, then re-validate.
//  5. Repeat until all transactions are validated or MaxValidationRounds is reached.
func (e *DAGExecutor) Run() []TxResult {
	if e.numTxs == 0 {
		return nil
	}

	// For very small blocks, sequential is simpler and faster.
	if e.numTxs <= 2 {
		e.runSequential()
		return e.results
	}

	// Phase 1: DAG-ordered execution in topological batches.
	batches := e.dag.Schedule(e.workers)
	for _, batch := range batches {
		if len(batch) == 1 {
			// Single tx in batch — execute directly without goroutine overhead.
			e.executeSingle(batch[0])
		} else {
			e.executeParallel(batch)
		}
	}

	// Phase 2: Block-STM validation as safety net.
	// Access lists may be incomplete (they don't cover dynamic SSTORE/SLOAD),
	// so we validate all transactions in order to catch missed dependencies.
	for round := 0; round < MaxValidationRounds; round++ {
		allValid := e.validateInOrder()
		if allValid {
			break
		}

		// Re-execute only the transactions marked as pending.
		pending := e.collectPending()
		if len(pending) == 0 {
			break
		}
		e.executeParallel(pending)
	}

	// Safety check: if not fully validated, fall back to sequential.
	if !e.allValidated() {
		log.Warn("DAGExecutor: validation rounds exhausted, sequential fallback",
			"txs", e.numTxs,
			"rounds", MaxValidationRounds,
			"executions", e.totalExecutions.Load(),
			"aborts", e.totalAborts.Load(),
		)
		e.mvs = NewMVS()
		e.resetStatus()
		e.runSequential()
		return e.results
	}

	log.Debug("DAGExecutor execution completed",
		"txs", e.numTxs,
		"batches", len(batches),
		"executions", e.totalExecutions.Load(),
		"aborts", e.totalAborts.Load(),
	)

	return e.results
}

// executeParallel runs the given tx indices concurrently using the worker pool.
func (e *DAGExecutor) executeParallel(txIndices []int) {
	var wg sync.WaitGroup
	work := make(chan int, len(txIndices))

	numWorkers := e.workers
	if numWorkers > len(txIndices) {
		numWorkers = len(txIndices)
	}

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					buf := make([]byte, 4096)
					n := runtime.Stack(buf, false)
					log.Error("panic in DAGExecutor worker, recovered",
						"panic", r, "stack", string(buf[:n]))
				}
			}()
			for txIndex := range work {
				e.executeSingle(txIndex)
			}
		}()
	}

	for _, idx := range txIndices {
		work <- idx
	}
	close(work)
	wg.Wait()
}

// executeSingle executes a single transaction and records its results in the MVS.
func (e *DAGExecutor) executeSingle(txIndex int) {
	e.totalExecutions.Add(1)

	rw := NewReadWriteSet(txIndex)
	e.mvs.DeleteAll(txIndex)

	err := e.execFn(txIndex, rw)

	e.results[txIndex] = TxResult{Err: err}
	e.rwSets[txIndex] = rw

	inc := e.incarnation[txIndex]
	for _, wd := range rw.Writes {
		e.mvs.Write(wd.Key, txIndex, inc, wd.Value)
	}

	e.status[txIndex] = StatusExecuted
}

// validateInOrder validates all executed transactions in order.
// On first failure, marks the failed tx and all later unvalidated txs as pending.
// Returns true if all txs pass validation.
func (e *DAGExecutor) validateInOrder() bool {
	for i := 0; i < e.numTxs; i++ {
		if e.status[i] == StatusValidated {
			continue
		}

		rw := e.rwSets[i]
		if Validate(e.mvs, rw) {
			e.status[i] = StatusValidated
		} else {
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

// collectPending returns indices of txs that need (re-)execution.
func (e *DAGExecutor) collectPending() []int {
	var pending []int
	for i := 0; i < e.numTxs; i++ {
		if e.status[i] != StatusValidated {
			pending = append(pending, i)
		}
	}
	return pending
}

// allValidated returns true if every transaction has been validated.
func (e *DAGExecutor) allValidated() bool {
	for i := 0; i < e.numTxs; i++ {
		if e.status[i] != StatusValidated {
			return false
		}
	}
	return true
}

// resetStatus resets all transaction statuses to pending (for sequential fallback).
func (e *DAGExecutor) resetStatus() {
	for i := range e.status {
		e.status[i] = StatusPending
		e.incarnation[i] = 0
	}
}

// runSequential executes all transactions sequentially as a fallback.
func (e *DAGExecutor) runSequential() {
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
func (e *DAGExecutor) MVS() *MVS {
	return e.mvs
}

// DAG returns the dependency graph (for testing/debugging).
func (e *DAGExecutor) DAG() *TxDAG {
	return e.dag
}

// Stats returns execution statistics: total executions and total aborts.
func (e *DAGExecutor) Stats() (executions, aborts int64) {
	return e.totalExecutions.Load(), e.totalAborts.Load()
}
