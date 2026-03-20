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

package deferred

import (
	"context"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// Pipeline implements a full consensus-execution pipeline where:
//
//	Block N:   consensus (ordering)  ←── current
//	Block N-1: execution (EVM)       ←── processing
//	Block N-2: commit (persist)      ←── finalizing
//
// This three-stage pipeline allows maximum overlap between consensus
// rounds and execution work, similar to Monad's architecture.
type Pipeline struct {
	executor *Executor

	// commitFn is called when a block's execution result should be persisted.
	commitFn CommitFunc

	// stateRootFn returns the state root for a given block (for the next block's header).
	// Returns zero hash and false if the block hasn't been executed yet.
	stateRootFn func(blockNumber uint64) (types.Hash, bool)

	mu             sync.RWMutex
	lastCommitted  uint64
	commitInterval time.Duration
}

// CommitFunc persists an execution result to the database.
type CommitFunc func(result *ExecutionResult) error

// PipelineConfig configures the deferred execution pipeline.
type PipelineConfig struct {
	// Execution config
	Execution Config
	// CommitInterval is how often to check for blocks ready to commit.
	CommitInterval time.Duration
}

// DefaultPipelineConfig returns sensible defaults.
func DefaultPipelineConfig() PipelineConfig {
	return PipelineConfig{
		Execution:      DefaultConfig(),
		CommitInterval: 100 * time.Millisecond,
	}
}

// NewPipeline creates a deferred execution pipeline.
func NewPipeline(config PipelineConfig, executeFn ExecuteFunc, commitFn CommitFunc) *Pipeline {
	if config.CommitInterval <= 0 {
		config.CommitInterval = DefaultPipelineConfig().CommitInterval
	}

	executor := New(config.Execution, executeFn)

	p := &Pipeline{
		executor:       executor,
		commitFn:       commitFn,
		commitInterval: config.CommitInterval,
	}

	// Wire up the state root lookup
	p.stateRootFn = func(blockNumber uint64) (types.Hash, bool) {
		result := executor.GetResult(blockNumber)
		if result == nil || result.Error != nil {
			return types.Hash{}, false
		}
		return result.StateRoot, true
	}

	return p
}

// Start launches the pipeline workers.
func (p *Pipeline) Start(ctx context.Context) {
	p.executor.Start()
	go p.commitLoop(ctx)
	log.Info("Deferred execution pipeline started")
}

// Stop gracefully shuts down the pipeline.
func (p *Pipeline) Stop() {
	p.executor.Stop()
	log.Info("Deferred execution pipeline stopped", "lastCommitted", p.lastCommitted)
}

// SubmitBlock queues a consensus-ordered block for deferred execution.
// Called by the consensus layer after block ordering is agreed upon.
func (p *Pipeline) SubmitBlock(blockNumber uint64, blockHash types.Hash) error {
	return p.executor.Submit(blockNumber, blockHash)
}

// StateRootForBlock returns the state root from executing block N.
// Used when building block N+1's header (deferred state root).
func (p *Pipeline) StateRootForBlock(blockNumber uint64) (types.Hash, bool) {
	return p.stateRootFn(blockNumber)
}

// WaitForExecution blocks until a specific block has been executed.
func (p *Pipeline) WaitForExecution(ctx context.Context, blockNumber uint64) (*ExecutionResult, error) {
	return p.executor.WaitForResult(ctx, blockNumber)
}

// Stats returns pipeline statistics.
func (p *Pipeline) Stats() PipelineStats {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return PipelineStats{
		LastQueued:   p.executor.LastQueuedBlock(),
		LastExecuted: p.executor.LastExecutedBlock(),
		LastCommitted: p.lastCommitted,
		QueueDepth:   p.executor.QueueDepth(),
	}
}

// PipelineStats holds pipeline health metrics.
type PipelineStats struct {
	LastQueued    uint64
	LastExecuted uint64
	LastCommitted uint64
	QueueDepth   int
}

// commitLoop periodically commits executed blocks in order.
func (p *Pipeline) commitLoop(ctx context.Context) {
	ticker := time.NewTicker(p.commitInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.tryCommit()
		}
	}
}

// tryCommit commits the next block in sequence if its execution is complete.
func (p *Pipeline) tryCommit() {
	p.mu.Lock()
	next := p.lastCommitted + 1
	p.mu.Unlock()

	result := p.executor.GetResult(next)
	if result == nil {
		return // Not executed yet
	}
	if result.Error != nil {
		log.Error("Cannot commit block with execution error",
			"block", next,
			"err", result.Error,
		)
		return
	}

	if p.commitFn != nil {
		if err := p.commitFn(result); err != nil {
			log.Error("Failed to commit deferred execution result",
				"block", next,
				"err", err,
			)
			return
		}
	}

	p.mu.Lock()
	p.lastCommitted = next
	p.mu.Unlock()

	// Prune old results to prevent memory growth
	if next > 128 {
		p.executor.PruneResults(next - 128)
	}

	log.Debug("Committed deferred execution",
		"block", next,
		"stateRoot", result.StateRoot.Hex()[:10],
		"elapsed", result.ExecutionTime,
	)
}
