package utils

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunEvery(t *testing.T) {
	var counter int32

	ctx, cancel := context.WithCancel(context.Background())

	// Run a function every 10ms
	RunEvery(ctx, 10*time.Millisecond, func() {
		atomic.AddInt32(&counter, 1)
	})

	// Wait for a few iterations
	time.Sleep(55 * time.Millisecond)

	// Cancel the context
	cancel()

	// Wait for goroutine to stop
	time.Sleep(20 * time.Millisecond)

	// Check that the function was called multiple times
	count := atomic.LoadInt32(&counter)
	if count < 3 {
		t.Errorf("RunEvery() called function %d times, expected at least 3", count)
	}

	// Store current count
	finalCount := count

	// Wait a bit more and verify it stopped
	time.Sleep(30 * time.Millisecond)
	newCount := atomic.LoadInt32(&counter)

	if newCount != finalCount {
		t.Errorf("RunEvery() continued after context cancelled: %d vs %d", finalCount, newCount)
	}
}

func TestRunEveryImmediateCancel(t *testing.T) {
	var counter int32

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	RunEvery(ctx, 10*time.Millisecond, func() {
		atomic.AddInt32(&counter, 1)
	})

	// Wait a bit
	time.Sleep(50 * time.Millisecond)

	// Function should not have been called (or maybe once due to timing)
	count := atomic.LoadInt32(&counter)
	if count > 1 {
		t.Errorf("RunEvery() called function %d times after immediate cancel", count)
	}
}
