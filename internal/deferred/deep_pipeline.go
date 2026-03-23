// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package deferred

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// DeepPipeline implements a 5-stage block processing pipeline inspired by
// Monad's Superscalar architecture:
//
//	Block N:   [Consensus]     — ordering agreed
//	Block N-1: [Prefetch]      — async state warming
//	Block N-2: [Execution]     — EVM processing
//	Block N-3: [Commitment]    — JMT update + state root computation
//	Block N-4: [Persistence]   — MDBX write transaction
//
// Each stage operates on a different block simultaneously, connected by typed
// channels. Backpressure propagates backward via channel blocking.
type DeepPipeline struct {
	// Stage channels (buffered).
	prefetchCh chan *DeepBlock
	executeCh  chan *DeepBlock
	commitCh   chan *DeepCommitJob
	persistCh  chan *DeepPersistJob

	// Stage functions (injected by caller).
	prefetchFn PrefetchFunc
	executeFn  DeepExecuteFunc
	commitFn   DeepCommitFunc
	persistFn  DeepPersistFunc

	// State tracking.
	lastPersisted atomic.Uint64
	stateRoots    sync.Map // blockNumber → types.Hash

	// Configuration.
	depth int // channel buffer size (default 4)

	// Lifecycle.
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running atomic.Bool
}

// DeepBlock is a block passed between pipeline stages.
type DeepBlock struct {
	Number uint64
	Hash   types.Hash
	Block  block.IBlock
}

// DeepCommitJob carries execution results to the commitment stage.
type DeepCommitJob struct {
	DeepBlock
	StateRoot     types.Hash
	GasUsed       uint64
	TxCount       int
	ExecutionTime time.Duration
}

// DeepPersistJob carries commitment results to the persistence stage.
type DeepPersistJob struct {
	DeepBlock
	StateRoot     types.Hash
	DirtyNodes    map[[32]byte][]byte // JMT dirty nodes snapshot
	GasUsed       uint64
	TxCount       int
	ExecutionTime time.Duration
}

// PrefetchFunc warms the state cache for the given block.
type PrefetchFunc func(ctx context.Context, blk block.IBlock) error

// DeepExecuteFunc executes a block and returns the result.
type DeepExecuteFunc func(ctx context.Context, blk block.IBlock) (*DeepCommitJob, error)

// DeepCommitFunc computes state commitment and returns data for persistence.
type DeepCommitFunc func(ctx context.Context, job *DeepCommitJob) (*DeepPersistJob, error)

// DeepPersistFunc writes the final result to the database.
type DeepPersistFunc func(ctx context.Context, job *DeepPersistJob) error

// DeepPipelineConfig configures the deep pipeline.
type DeepPipelineConfig struct {
	Depth int // Channel buffer size (default 4)
}

// NewDeepPipeline creates a 5-stage pipeline.
func NewDeepPipeline(
	cfg DeepPipelineConfig,
	prefetchFn PrefetchFunc,
	executeFn DeepExecuteFunc,
	commitFn DeepCommitFunc,
	persistFn DeepPersistFunc,
) *DeepPipeline {
	depth := cfg.Depth
	if depth <= 0 {
		depth = 4
	}
	return &DeepPipeline{
		prefetchCh: make(chan *DeepBlock, depth),
		executeCh:  make(chan *DeepBlock, depth),
		commitCh:   make(chan *DeepCommitJob, depth),
		persistCh:  make(chan *DeepPersistJob, depth),
		prefetchFn: prefetchFn,
		executeFn:  executeFn,
		commitFn:   commitFn,
		persistFn:  persistFn,
		depth:      depth,
	}
}

// Start launches all pipeline stage goroutines. No-op if already running.
func (p *DeepPipeline) Start(ctx context.Context) {
	if !p.running.CompareAndSwap(false, true) {
		return
	}
	p.ctx, p.cancel = context.WithCancel(ctx)

	p.wg.Add(4)
	go p.runPrefetch()
	go p.runExecute()
	go p.runCommit()
	go p.runPersist()

	log.Info("Deep pipeline started", "stages", 5, "depth", p.depth)
}

// SubmitBlock submits a block into the pipeline's first stage.
func (p *DeepPipeline) SubmitBlock(blk block.IBlock) error {
	num := blk.Number64()
	var blockNum uint64
	if num != nil {
		blockNum = num.Uint64()
	}
	db := &DeepBlock{
		Number: blockNum,
		Hash:   blk.Hash(),
		Block:  blk,
	}
	select {
	case p.prefetchCh <- db:
		return nil
	case <-p.ctx.Done():
		return p.ctx.Err()
	}
}

// StateRootForBlock returns the state root computed for the given block.
func (p *DeepPipeline) StateRootForBlock(blockNumber uint64) (types.Hash, bool) {
	if v, ok := p.stateRoots.Load(blockNumber); ok {
		return v.(types.Hash), true
	}
	return types.Hash{}, false
}

// LastPersisted returns the highest block number that has been fully persisted.
func (p *DeepPipeline) LastPersisted() uint64 {
	return p.lastPersisted.Load()
}

// Stop gracefully shuts down all pipeline stages.
// Uses context cancellation only — does not close prefetchCh to avoid
// race with concurrent SubmitBlock calls.
func (p *DeepPipeline) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
	p.running.Store(false)
	log.Info("Deep pipeline stopped")
}

// --- Stage goroutines ---

func (p *DeepPipeline) runPrefetch() {
	defer p.wg.Done()
	defer close(p.executeCh)

	for {
		select {
		case blk, ok := <-p.prefetchCh:
			if !ok {
				return
			}
			if p.prefetchFn != nil {
				if err := p.prefetchFn(p.ctx, blk.Block); err != nil {
					log.Warn("Deep pipeline prefetch failed", "block", blk.Number, "err", err)
				}
			}
			select {
			case p.executeCh <- blk:
			case <-p.ctx.Done():
				return
			}
		case <-p.ctx.Done():
			return
		}
	}
}

func (p *DeepPipeline) runExecute() {
	defer p.wg.Done()
	defer close(p.commitCh)

	for {
		select {
		case blk, ok := <-p.executeCh:
			if !ok {
				return
			}
			if p.executeFn == nil {
				continue
			}
			result, err := p.executeFn(p.ctx, blk.Block)
			if err != nil {
				log.Error("Deep pipeline execution failed — halting pipeline", "block", blk.Number, "err", err)
				p.cancel()
				return
			}
			select {
			case p.commitCh <- result:
			case <-p.ctx.Done():
				return
			}
		case <-p.ctx.Done():
			return
		}
	}
}

func (p *DeepPipeline) runCommit() {
	defer p.wg.Done()
	defer close(p.persistCh)

	for {
		select {
		case job, ok := <-p.commitCh:
			if !ok {
				return
			}
			if p.commitFn == nil {
				continue
			}
			result, err := p.commitFn(p.ctx, job)
			if err != nil {
				log.Error("Deep pipeline commitment failed — halting pipeline", "block", job.Number, "err", err)
				p.cancel()
				return
			}
			p.stateRoots.Store(job.Number, result.StateRoot)
			select {
			case p.persistCh <- result:
			case <-p.ctx.Done():
				return
			}
		case <-p.ctx.Done():
			return
		}
	}
}

func (p *DeepPipeline) runPersist() {
	defer p.wg.Done()

	for {
		select {
		case job, ok := <-p.persistCh:
			if !ok {
				return
			}
			if p.persistFn == nil {
				continue
			}
			if err := p.persistFn(p.ctx, job); err != nil {
				log.Error("Deep pipeline persistence failed", "block", job.Number, "err", err)
				continue
			}
			p.lastPersisted.Store(job.Number)

			// Prune old state roots to prevent unbounded memory growth.
			if job.Number > 128 {
				p.stateRoots.Delete(job.Number - 128)
			}

			log.Debug("Deep pipeline block persisted",
				"block", job.Number,
				"gasUsed", job.GasUsed,
				"txCount", job.TxCount,
				"execTime", job.ExecutionTime,
			)
		case <-p.ctx.Done():
			return
		}
	}
}

// Reset drains all pipeline channels and resets state for reorg recovery.
func (p *DeepPipeline) Reset(ctx context.Context, forkBlock uint64) {
	// Cancel current work.
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()

	// Clear state roots beyond the fork point.
	p.stateRoots.Range(func(key, _ any) bool {
		if key.(uint64) > forkBlock {
			p.stateRoots.Delete(key)
		}
		return true
	})
	p.lastPersisted.Store(forkBlock)

	// Recreate channels and restart.
	p.prefetchCh = make(chan *DeepBlock, p.depth)
	p.executeCh = make(chan *DeepBlock, p.depth)
	p.commitCh = make(chan *DeepCommitJob, p.depth)
	p.persistCh = make(chan *DeepPersistJob, p.depth)

	p.Start(ctx)
}
