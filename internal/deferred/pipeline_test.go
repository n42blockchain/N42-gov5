package deferred

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func TestPipelineStartStop(t *testing.T) {
	p := NewPipeline(DefaultPipelineConfig(), mockExecuteFn, NewCommitFunc())
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)

	// Verify it's running
	stats := p.Stats()
	if stats.LastQueued != 0 || stats.LastExecuted != 0 {
		t.Errorf("fresh pipeline should have zero stats, got %+v", stats)
	}

	cancel()
	p.Stop()
}

func TestPipelineSubmitAndCommit(t *testing.T) {
	var committed atomic.Int32
	commitFn := func(r *ExecutionResult) error {
		committed.Add(1)
		return nil
	}

	cfg := DefaultPipelineConfig()
	cfg.CommitInterval = 50 * time.Millisecond
	p := NewPipeline(cfg, mockExecuteFn, commitFn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	defer p.Stop()

	// Submit blocks 1-5
	for i := uint64(1); i <= 5; i++ {
		if err := p.SubmitBlock(i, types.Hash{}); err != nil {
			t.Fatalf("SubmitBlock(%d): %v", i, err)
		}
	}

	// Wait for execution + commit
	time.Sleep(500 * time.Millisecond)

	if committed.Load() < 3 {
		t.Errorf("expected at least 3 commits, got %d", committed.Load())
	}

	stats := p.Stats()
	t.Logf("Stats: queued=%d executed=%d committed=%d queue=%d",
		stats.LastQueued, stats.LastExecuted, stats.LastCommitted, stats.QueueDepth)
}

func TestPipelineStateRoot(t *testing.T) {
	p := NewPipeline(DefaultPipelineConfig(), mockExecuteFn, NewCommitFunc())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	defer p.Stop()

	// Before execution, state root should not be available
	_, ok := p.StateRootForBlock(1)
	if ok {
		t.Error("state root should not be available before execution")
	}

	p.SubmitBlock(1, types.Hash{})

	// Wait for execution
	execCtx, execCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer execCancel()
	result, err := p.WaitForExecution(execCtx, 1)
	if err != nil {
		t.Fatalf("WaitForExecution: %v", err)
	}
	if result == nil {
		t.Fatal("expected result")
	}

	// Now state root should be available
	root, ok := p.StateRootForBlock(1)
	if !ok {
		t.Error("state root should be available after execution")
	}
	if root == (types.Hash{}) {
		t.Error("state root should not be zero")
	}
}

func TestPipelineCommitError(t *testing.T) {
	failCommit := func(r *ExecutionResult) error {
		return fmt.Errorf("commit failed")
	}

	cfg := DefaultPipelineConfig()
	cfg.CommitInterval = 50 * time.Millisecond
	p := NewPipeline(cfg, mockExecuteFn, failCommit)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	defer p.Stop()

	p.SubmitBlock(1, types.Hash{})
	time.Sleep(300 * time.Millisecond)

	// Commit should have failed, lastCommitted stays at 0
	stats := p.Stats()
	if stats.LastCommitted != 0 {
		t.Errorf("lastCommitted should be 0 after commit error, got %d", stats.LastCommitted)
	}
}

func TestPipelineDoubleStop(t *testing.T) {
	p := NewPipeline(DefaultPipelineConfig(), mockExecuteFn, NewCommitFunc())
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	cancel()
	p.Stop()
	p.Stop() // should not panic
}
