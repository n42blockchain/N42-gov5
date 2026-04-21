// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package common

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestAtomicGasPool_Basic(t *testing.T) {
	p := NewAtomicGasPool(1000)
	if p.Gas() != 1000 {
		t.Fatalf("initial: got %d want 1000", p.Gas())
	}
	if err := p.SubGas(300); err != nil {
		t.Fatalf("SubGas(300): %v", err)
	}
	if p.Gas() != 700 {
		t.Fatalf("after sub: got %d want 700", p.Gas())
	}
	p.AddGas(500)
	if p.Gas() != 1200 {
		t.Fatalf("after add: got %d want 1200", p.Gas())
	}
}

func TestAtomicGasPool_Exhausted(t *testing.T) {
	p := NewAtomicGasPool(100)
	if err := p.SubGas(150); !errors.Is(err, ErrGasLimitReached) {
		t.Errorf("SubGas(150) on 100: got %v want ErrGasLimitReached", err)
	}
	// Pool should be unchanged.
	if p.Gas() != 100 {
		t.Errorf("after failed sub: got %d want 100", p.Gas())
	}
}

func TestAtomicGasPool_OverflowPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on overflow")
		}
	}()
	p := NewAtomicGasPool(^uint64(0))
	p.AddGas(1) // must panic
}

func TestAtomicGasPool_ConcurrentSub(t *testing.T) {
	// N workers each subtract 1 gas until exhausted. Exact count of
	// successful subs must equal the initial pool size.
	const initial = 10_000
	const workers = 64
	p := NewAtomicGasPool(initial)

	var success atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				err := p.SubGas(1)
				if err != nil {
					return
				}
				success.Add(1)
			}
		}()
	}
	wg.Wait()

	if success.Load() != int64(initial) {
		t.Errorf("successful subs=%d want %d", success.Load(), initial)
	}
	if p.Gas() != 0 {
		t.Errorf("final gas=%d want 0", p.Gas())
	}
}

func TestAtomicGasPool_ConcurrentAddSub(t *testing.T) {
	// Half workers add, half subtract. Final gas = initial (all adds
	// cancel subs). Tests that both ops are atomic w.r.t. each other.
	const initial = 100_000
	const iters = 10_000
	const perSide = 32
	p := NewAtomicGasPool(initial)

	var wg sync.WaitGroup
	for i := 0; i < perSide; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				p.AddGas(7)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				if err := p.SubGas(7); err != nil {
					// Pool empty for a moment; retry later.
					for p.SubGas(7) == ErrGasLimitReached {
					}
				}
			}
		}()
	}
	wg.Wait()

	if p.Gas() != initial {
		t.Errorf("final gas=%d want %d", p.Gas(), initial)
	}
}

func BenchmarkAtomicGasPool_SubGas(b *testing.B) {
	p := NewAtomicGasPool(^uint64(0))
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = p.SubGas(1)
		}
	})
}
