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
// Package-level doc plus the core asynchronous Executor. Defines
// ExecutionResult (block number, hash, post-exec state root, gas, tx
// count) and the worker-pool driven executor that dequeues ordered
// blocks from the consensus layer, runs the supplied ExecuteFunc, and
// publishes results so block N+1's header can carry state root of N.

// Package deferred implements deferred (asynchronous) block execution,
// inspired by Monad and Aptos architectures.
//
// In deferred execution mode, consensus and execution are decoupled:
//
//   - Consensus layer: agrees on transaction ordering (block = ordered tx list)
//   - Execution layer: processes transactions asynchronously in the background
//   - State root: the state root for block N is included in block N+1's header
//
// This allows the consensus pipeline to proceed at full speed without waiting
// for EVM execution, and lets the execution layer use the full block interval
// to process transactions (including parallel execution via Block-STM).
//
// Architecture:
//
//	Consensus ──► OrderedBlock ──► ExecutionQueue ──► EVM Worker Pool
//	                                                      │
//	                                                      ▼
//	                                               ExecutionResult
//	                                               (stateRoot, receipts)
//	                                                      │
//	Block N+1 header ◄────────── stateRoot(N) ◄───────────┘
package deferred

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// ExecutionResult holds the outcome of executing a block's transactions.
type ExecutionResult struct {
	// BlockNumber is the block that was executed.
	BlockNumber uint64
	// BlockHash is the consensus-agreed block hash.
	BlockHash types.Hash
	// StateRoot is the post-execution state root (for inclusion in the next block).
	StateRoot types.Hash
	// GasUsed is the total gas consumed by all transactions.
	GasUsed uint64
	// TxCount is the number of transactions executed.
	TxCount int
	// ExecutionTime is how long the execution took.
	ExecutionTime time.Duration
	// Error is non-nil if execution failed.
	Error error
}

// ExecuteFunc is the function signature for block execution.
// It receives a block number and hash, and returns the execution result.
// The implementation should perform full EVM execution including:
// - Transaction processing (sequential or parallel via Block-STM)
// - State transitions and receipt generation
// - State root computation
type ExecuteFunc func(ctx context.Context, blockNumber uint64, blockHash types.Hash) (*ExecutionResult, error)

// Config holds configuration for the deferred executor.
type Config struct {
	// QueueSize is the maximum number of blocks that can be queued for execution.
	// Once full, consensus will block until execution catches up.
	QueueSize int
	// Workers is the number of concurrent execution workers.
	// Typically 1 for sequential consistency, but can be >1 if blocks
	// are independent (e.g., different shards).
	Workers int
}

// DefaultConfig returns sensible defaults for deferred execution.
func DefaultConfig() Config {
	return Config{
		QueueSize: 64,
		Workers:   1,
	}
}

// Executor manages the deferred execution pipeline.
// It maintains an ordered queue of blocks awaiting execution and
// tracks which blocks have been executed with their results.
type Executor struct {
	config    Config
	executeFn ExecuteFunc

	// queue holds blocks waiting to be executed, in order.
	queue chan executionJob

	// results stores completed execution results by block number.
	mu      sync.RWMutex
	results map[uint64]*ExecutionResult

	// lastExecuted tracks the highest block number that has been executed.
	lastExecuted atomic.Uint64
	// lastQueued tracks the highest block number queued for execution.
	lastQueued atomic.Uint64

	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopOnce sync.Once // guards double-close of queue channel
}

type executionJob struct {
	blockNumber uint64
	blockHash   types.Hash
}

// New creates a new deferred Executor with the given execution function.
func New(config Config, executeFn ExecuteFunc) *Executor {
	if config.QueueSize <= 0 {
		config.QueueSize = DefaultConfig().QueueSize
	}
	if config.Workers <= 0 {
		config.Workers = DefaultConfig().Workers
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Executor{
		config:    config,
		executeFn: executeFn,
		queue:     make(chan executionJob, config.QueueSize),
		results:   make(map[uint64]*ExecutionResult),
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Start launches the execution worker goroutines.
func (e *Executor) Start() {
	for i := 0; i < e.config.Workers; i++ {
		e.wg.Add(1)
		go e.worker(i)
	}
	log.Info("Deferred executor started", "workers", e.config.Workers, "queueSize", e.config.QueueSize)
}

// Stop gracefully shuts down the executor, waiting for in-flight work.
// Safe to call multiple times.
func (e *Executor) Stop() {
	e.cancel()
	e.stopOnce.Do(func() { close(e.queue) })
	e.wg.Wait()
	log.Info("Deferred executor stopped", "lastExecuted", e.lastExecuted.Load())
}

// Submit queues a block for deferred execution.
// This is called by the consensus layer after agreeing on transaction ordering.
// It blocks if the queue is full (backpressure from execution to consensus).
func (e *Executor) Submit(blockNumber uint64, blockHash types.Hash) error {
	// Check context first to avoid send-on-closed-channel panic
	// when Stop() has been called concurrently.
	if e.ctx.Err() != nil {
		return e.ctx.Err()
	}
	select {
	case e.queue <- executionJob{blockNumber: blockNumber, blockHash: blockHash}:
		e.lastQueued.Store(blockNumber)
		return nil
	case <-e.ctx.Done():
		return e.ctx.Err()
	}
}

// GetResult returns the execution result for a given block number.
// Returns nil if the block hasn't been executed yet.
func (e *Executor) GetResult(blockNumber uint64) *ExecutionResult {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.results[blockNumber]
}

// WaitForResult blocks until the execution result for the given block
// is available, or the context is cancelled.
func (e *Executor) WaitForResult(ctx context.Context, blockNumber uint64) (*ExecutionResult, error) {
	// Fast path: already executed
	if result := e.GetResult(blockNumber); result != nil {
		return result, nil
	}

	// Poll with exponential backoff (simple approach)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			if result := e.GetResult(blockNumber); result != nil {
				return result, nil
			}
		}
	}
}

// LastExecutedBlock returns the highest block number that has been executed.
func (e *Executor) LastExecutedBlock() uint64 {
	return e.lastExecuted.Load()
}

// LastQueuedBlock returns the highest block number queued for execution.
func (e *Executor) LastQueuedBlock() uint64 {
	return e.lastQueued.Load()
}

// QueueDepth returns the number of blocks waiting for execution.
func (e *Executor) QueueDepth() int {
	return len(e.queue)
}

// PruneResults removes execution results for blocks below the given number.
// This should be called periodically to prevent unbounded memory growth.
func (e *Executor) PruneResults(below uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for num := range e.results {
		if num < below {
			delete(e.results, num)
		}
	}
}

// worker is the goroutine that processes queued blocks.
func (e *Executor) worker(id int) {
	defer e.wg.Done()

	for job := range e.queue {
		start := time.Now()
		result, err := e.executeFn(e.ctx, job.blockNumber, job.blockHash)
		if err != nil || result == nil {
			errMsg := err
			if errMsg == nil {
				errMsg = fmt.Errorf("executeFn returned nil result")
			}
			result = &ExecutionResult{
				BlockNumber:   job.blockNumber,
				BlockHash:     job.blockHash,
				Error:         fmt.Errorf("execution failed: %w", errMsg),
				ExecutionTime: time.Since(start),
			}
		} else {
			result.ExecutionTime = time.Since(start)
		}

		e.mu.Lock()
		e.results[job.blockNumber] = result
		e.mu.Unlock()

		// Monotonic update: only advance lastExecuted if this block is higher
		for {
			old := e.lastExecuted.Load()
			if job.blockNumber <= old {
				break
			}
			if e.lastExecuted.CompareAndSwap(old, job.blockNumber) {
				break
			}
		}

		if result.Error != nil {
			log.Error("Deferred execution failed",
				"worker", id,
				"block", job.blockNumber,
				"err", result.Error,
				"elapsed", result.ExecutionTime,
			)
		} else {
			log.Debug("Deferred execution complete",
				"worker", id,
				"block", job.blockNumber,
				"stateRoot", result.StateRoot.Hex()[:10],
				"gasUsed", result.GasUsed,
				"txs", result.TxCount,
				"elapsed", result.ExecutionTime,
			)
		}
	}
}
