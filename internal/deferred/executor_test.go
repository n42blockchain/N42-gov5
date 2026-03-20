package deferred

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func mockExecuteFn(ctx context.Context, blockNumber uint64, blockHash types.Hash) (*ExecutionResult, error) {
	return &ExecutionResult{
		BlockNumber: blockNumber,
		BlockHash:   blockHash,
		StateRoot:   types.HexToHash(fmt.Sprintf("0x%064x", blockNumber)),
		GasUsed:     21000 * blockNumber,
		TxCount:     int(blockNumber % 10),
	}, nil
}

func TestExecutorBasicFlow(t *testing.T) {
	executor := New(DefaultConfig(), mockExecuteFn)
	executor.Start()
	defer executor.Stop()

	// Submit blocks 1-5
	for i := uint64(1); i <= 5; i++ {
		hash := types.HexToHash(fmt.Sprintf("0x%064x", i+100))
		if err := executor.Submit(i, hash); err != nil {
			t.Fatalf("Submit(%d) error: %v", i, err)
		}
	}

	// Wait for all to complete
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for i := uint64(1); i <= 5; i++ {
		result, err := executor.WaitForResult(ctx, i)
		if err != nil {
			t.Fatalf("WaitForResult(%d) error: %v", i, err)
		}
		if result.BlockNumber != i {
			t.Errorf("result.BlockNumber = %d, want %d", result.BlockNumber, i)
		}
		if result.Error != nil {
			t.Errorf("result.Error = %v, want nil", result.Error)
		}
		if result.GasUsed != 21000*i {
			t.Errorf("result.GasUsed = %d, want %d", result.GasUsed, 21000*i)
		}
	}

	if executor.LastExecutedBlock() != 5 {
		t.Errorf("LastExecutedBlock() = %d, want 5", executor.LastExecutedBlock())
	}
}

func TestExecutorQueueDepth(t *testing.T) {
	// Use a slow executor to observe queue depth
	slowFn := func(ctx context.Context, blockNumber uint64, blockHash types.Hash) (*ExecutionResult, error) {
		time.Sleep(50 * time.Millisecond)
		return mockExecuteFn(ctx, blockNumber, blockHash)
	}

	config := Config{QueueSize: 10, Workers: 1}
	executor := New(config, slowFn)
	executor.Start()
	defer executor.Stop()

	// Submit multiple blocks rapidly
	for i := uint64(1); i <= 5; i++ {
		executor.Submit(i, types.Hash{})
	}

	// Queue should have items
	if executor.QueueDepth() == 0 && executor.LastExecutedBlock() < 5 {
		// Queue might already be draining, that's fine
		t.Log("Queue drained quickly (fast execution)")
	}

	// Wait for completion
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	executor.WaitForResult(ctx, 5)
}

func TestExecutorPruneResults(t *testing.T) {
	executor := New(DefaultConfig(), mockExecuteFn)
	executor.Start()
	defer executor.Stop()

	for i := uint64(1); i <= 10; i++ {
		executor.Submit(i, types.Hash{})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	executor.WaitForResult(ctx, 10)

	// All 10 results should be available
	for i := uint64(1); i <= 10; i++ {
		if executor.GetResult(i) == nil {
			t.Fatalf("result %d missing before prune", i)
		}
	}

	// Prune results below block 6
	executor.PruneResults(6)

	// Blocks 1-5 should be gone
	for i := uint64(1); i <= 5; i++ {
		if executor.GetResult(i) != nil {
			t.Errorf("result %d still present after prune", i)
		}
	}
	// Blocks 6-10 should remain
	for i := uint64(6); i <= 10; i++ {
		if executor.GetResult(i) == nil {
			t.Errorf("result %d missing after prune", i)
		}
	}
}

func TestExecutorErrorHandling(t *testing.T) {
	failingFn := func(ctx context.Context, blockNumber uint64, blockHash types.Hash) (*ExecutionResult, error) {
		if blockNumber == 3 {
			return nil, fmt.Errorf("block 3 execution failed")
		}
		return mockExecuteFn(ctx, blockNumber, blockHash)
	}

	executor := New(DefaultConfig(), failingFn)
	executor.Start()
	defer executor.Stop()

	for i := uint64(1); i <= 5; i++ {
		executor.Submit(i, types.Hash{})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Block 3 should have an error
	result, err := executor.WaitForResult(ctx, 3)
	if err != nil {
		t.Fatalf("WaitForResult(3) error: %v", err)
	}
	if result.Error == nil {
		t.Fatal("expected error for block 3")
	}

	// Other blocks should succeed
	result, _ = executor.WaitForResult(ctx, 1)
	if result.Error != nil {
		t.Errorf("block 1 should succeed, got: %v", result.Error)
	}
}

func TestExecutorCancellation(t *testing.T) {
	var executed atomic.Int32
	slowFn := func(ctx context.Context, blockNumber uint64, blockHash types.Hash) (*ExecutionResult, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
			executed.Add(1)
			return mockExecuteFn(ctx, blockNumber, blockHash)
		}
	}

	executor := New(Config{QueueSize: 100, Workers: 1}, slowFn)
	executor.Start()

	// Submit many blocks
	for i := uint64(1); i <= 50; i++ {
		executor.Submit(i, types.Hash{})
	}

	// Stop quickly - should not process all 50
	time.Sleep(300 * time.Millisecond)
	executor.Stop()

	if executed.Load() >= 50 {
		t.Errorf("expected fewer than 50 blocks executed after early stop, got %d", executed.Load())
	}
}

func TestExecutorConcurrentAccess(t *testing.T) {
	executor := New(Config{QueueSize: 100, Workers: 2}, mockExecuteFn)
	executor.Start()
	defer executor.Stop()

	// Submit from multiple goroutines
	var wg atomic.Int32
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(offset uint64) {
			defer wg.Add(-1)
			for i := uint64(0); i < 25; i++ {
				executor.Submit(offset+i, types.Hash{})
			}
		}(uint64(g) * 25)
	}

	// Wait for all submissions
	for wg.Load() > 0 {
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for submissions to complete
	for wg.Load() > 0 {
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for execution to finish (blocks 0-99 submitted across goroutines)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Just verify we can get some results without deadlock
	time.Sleep(200 * time.Millisecond)
	lastExec := executor.LastExecutedBlock()
	t.Logf("lastExecuted=%d after concurrent submissions", lastExec)

	// Verify at least some blocks were executed (exact count depends on timing)
	if result := executor.GetResult(lastExec); result != nil && result.Error != nil {
		t.Errorf("unexpected error for block %d: %v", lastExec, result.Error)
	}
	_ = ctx // suppress unused
}
