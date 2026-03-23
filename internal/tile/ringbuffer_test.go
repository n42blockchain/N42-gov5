// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package tile

import (
	"runtime"
	"sync"
	"testing"
)

func TestRingBuffer_BasicPushPop(t *testing.T) {
	rb := NewRingBuffer(8)

	if !rb.TryPush("hello") {
		t.Fatal("push to empty buffer should succeed")
	}
	if rb.Len() != 1 {
		t.Fatalf("expected len 1, got %d", rb.Len())
	}

	item, ok := rb.TryPop()
	if !ok || item.(string) != "hello" {
		t.Fatalf("expected 'hello', got %v (ok=%v)", item, ok)
	}
	if rb.Len() != 0 {
		t.Fatalf("expected len 0, got %d", rb.Len())
	}
}

func TestRingBuffer_Empty(t *testing.T) {
	rb := NewRingBuffer(8)
	item, ok := rb.TryPop()
	if ok || item != nil {
		t.Fatal("pop from empty buffer should return nil, false")
	}
	if !rb.IsEmpty() {
		t.Fatal("new buffer should be empty")
	}
}

func TestRingBuffer_Full(t *testing.T) {
	rb := NewRingBuffer(4) // rounds to 4
	for i := 0; i < 4; i++ {
		if !rb.TryPush(i) {
			t.Fatalf("push %d should succeed", i)
		}
	}
	if !rb.IsFull() {
		t.Fatal("buffer should be full after 4 pushes")
	}
	if rb.TryPush(99) {
		t.Fatal("push to full buffer should fail")
	}
}

func TestRingBuffer_WrapAround(t *testing.T) {
	rb := NewRingBuffer(4)

	// Fill and drain twice to test wrap-around.
	for round := 0; round < 3; round++ {
		for i := 0; i < 4; i++ {
			if !rb.TryPush(round*4 + i) {
				t.Fatalf("round %d push %d failed", round, i)
			}
		}
		for i := 0; i < 4; i++ {
			item, ok := rb.TryPop()
			if !ok {
				t.Fatalf("round %d pop %d failed", round, i)
			}
			expected := round*4 + i
			if item.(int) != expected {
				t.Fatalf("round %d pop %d: expected %d, got %d", round, i, expected, item)
			}
		}
	}
}

func TestRingBuffer_PowerOfTwo(t *testing.T) {
	tests := []struct{ input, expected int }{
		{1, 2}, {2, 2}, {3, 4}, {4, 4}, {5, 8}, {7, 8}, {8, 8},
		{9, 16}, {100, 128}, {1000, 1024}, {1025, 2048},
	}
	for _, tt := range tests {
		rb := NewRingBuffer(tt.input)
		if rb.Cap() != tt.expected {
			t.Errorf("NewRingBuffer(%d).Cap() = %d, want %d", tt.input, rb.Cap(), tt.expected)
		}
	}
}

func TestRingBuffer_SPSC_Ordering(t *testing.T) {
	const N = 100000
	rb := NewRingBuffer(1024)

	var wg sync.WaitGroup
	wg.Add(2)

	// Producer
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			for !rb.TryPush(i) {
				runtime.Gosched()
			}
		}
	}()

	// Consumer
	results := make([]int, 0, N)
	go func() {
		defer wg.Done()
		for len(results) < N {
			item, ok := rb.TryPop()
			if ok {
				results = append(results, item.(int))
			} else {
				runtime.Gosched()
			}
		}
	}()

	wg.Wait()

	if len(results) != N {
		t.Fatalf("expected %d items, got %d", N, len(results))
	}
	for i, v := range results {
		if v != i {
			t.Fatalf("ordering violated at index %d: expected %d, got %d", i, i, v)
		}
	}
}

func TestRingBuffer_SPSC_HighContention(t *testing.T) {
	const N = 1000000
	rb := NewRingBuffer(64) // small buffer = high contention

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			for !rb.TryPush(i) {
				runtime.Gosched()
			}
		}
	}()

	count := 0
	go func() {
		defer wg.Done()
		for count < N {
			if _, ok := rb.TryPop(); ok {
				count++
			} else {
				runtime.Gosched()
			}
		}
	}()

	wg.Wait()
	if count != N {
		t.Fatalf("expected %d items consumed, got %d", N, count)
	}
}

func BenchmarkRingBuffer_SPSC(b *testing.B) {
	rb := NewRingBuffer(1024)
	b.RunParallel(func(pb *testing.PB) {
		// This benchmark is not truly SPSC since RunParallel uses multiple goroutines.
		// Use dedicated producer/consumer benchmark below for accurate SPSC numbers.
		for pb.Next() {
			rb.TryPush(42)
			rb.TryPop()
		}
	})
}

func BenchmarkRingBuffer_SPSC_Throughput(b *testing.B) {
	rb := NewRingBuffer(1024)
	done := make(chan struct{})

	// Consumer goroutine
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				rb.TryPop()
			}
		}
	}()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for !rb.TryPush(i) {
			runtime.Gosched()
		}
	}
	b.StopTimer()
	close(done)
}

func BenchmarkChannel_Throughput(b *testing.B) {
	ch := make(chan int, 1024)
	done := make(chan struct{})

	go func() {
		for {
			select {
			case <-done:
				return
			case <-ch:
			}
		}
	}()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ch <- i
	}
	b.StopTimer()
	close(done)
}
