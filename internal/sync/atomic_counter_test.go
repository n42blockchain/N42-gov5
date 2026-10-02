package sync

import "testing"

func TestAtomicInt64(t *testing.T) {
	a := NewAtomicInt64(5)
	if got := a.Load(); got != 5 {
		t.Fatalf("Load() = %d, want 5", got)
	}
	a.Store(10)
	if got := a.Load(); got != 10 {
		t.Fatalf("Load() = %d, want 10", got)
	}
	if got := a.Add(5); got != 15 {
		t.Fatalf("Add(5) = %d, want 15", got)
	}
	if got := a.Inc(); got != 16 {
		t.Fatalf("Inc() = %d, want 16", got)
	}
	if got := a.Dec(); got != 15 {
		t.Fatalf("Dec() = %d, want 15", got)
	}
	if !a.CompareAndSwap(15, 20) {
		t.Fatalf("expected CompareAndSwap to succeed")
	}
	if a.CompareAndSwap(15, 99) {
		t.Fatalf("expected CompareAndSwap to fail on stale old value")
	}
	if got := a.Load(); got != 20 {
		t.Fatalf("Load() = %d, want 20", got)
	}
}

func TestAtomicUint64(t *testing.T) {
	a := NewAtomicUint64(5)
	if got := a.Load(); got != 5 {
		t.Fatalf("Load() = %d, want 5", got)
	}
	a.Store(10)
	if got := a.Load(); got != 10 {
		t.Fatalf("Load() = %d, want 10", got)
	}
	if got := a.Add(5); got != 15 {
		t.Fatalf("Add(5) = %d, want 15", got)
	}
	if got := a.Inc(); got != 16 {
		t.Fatalf("Inc() = %d, want 16", got)
	}
	if !a.CompareAndSwap(16, 25) {
		t.Fatalf("expected CompareAndSwap to succeed")
	}
	if a.CompareAndSwap(16, 99) {
		t.Fatalf("expected CompareAndSwap to fail on stale old value")
	}
	if got := a.Load(); got != 25 {
		t.Fatalf("Load() = %d, want 25", got)
	}
}

func TestAtomicBool(t *testing.T) {
	b := NewAtomicBool(false)
	if b.Load() {
		t.Fatalf("expected initial value false")
	}
	b.Store(true)
	if !b.Load() {
		t.Fatalf("expected value true after Store")
	}
	if !b.CompareAndSwap(true, false) {
		t.Fatalf("expected CompareAndSwap to succeed")
	}
	if b.Load() {
		t.Fatalf("expected value false after CompareAndSwap")
	}
	if b.CompareAndSwap(true, true) {
		t.Fatalf("expected CompareAndSwap to fail on stale old value")
	}

	if got := b.Toggle(); got != true {
		t.Fatalf("Toggle() = %v, want true", got)
	}
	if got := b.Toggle(); got != false {
		t.Fatalf("Toggle() = %v, want false", got)
	}
}
