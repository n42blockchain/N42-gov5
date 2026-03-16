package utils

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitForCounterAtLeast(t *testing.T, counter *int32, want int32, timeout time.Duration) int32 {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		got := atomic.LoadInt32(counter)
		if got >= want {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	return atomic.LoadInt32(counter)
}

func TestRunEvery(t *testing.T) {
	var counter int32
	var wg sync.WaitGroup

	ctx, cancel := context.WithCancel(context.Background())
	wg.Add(1)

	RunEveryWithWG(ctx, 10*time.Millisecond, func() {
		atomic.AddInt32(&counter, 1)
	}, &wg)

	count := waitForCounterAtLeast(t, &counter, 3, time.Second)
	if count < 3 {
		t.Errorf("RunEvery() called function %d times, expected at least 3", count)
	}

	cancel()
	waitGroupOrTimeout(t, &wg, time.Second, "RunEvery goroutine shutdown")

	if finalCount := atomic.LoadInt32(&counter); finalCount < count {
		t.Errorf("RunEvery() counter regressed after shutdown: %d -> %d", count, finalCount)
	}
}

func TestRunEveryImmediateCancel(t *testing.T) {
	var counter int32
	var wg sync.WaitGroup

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wg.Add(1)

	RunEveryWithWG(ctx, 10*time.Millisecond, func() {
		atomic.AddInt32(&counter, 1)
	}, &wg)

	waitGroupOrTimeout(t, &wg, time.Second, "RunEvery immediate-cancel exit")

	if count := atomic.LoadInt32(&counter); count != 0 {
		t.Errorf("RunEvery() called function %d times after immediate cancel", count)
	}
}
