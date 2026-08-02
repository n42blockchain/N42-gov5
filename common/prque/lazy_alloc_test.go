// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// A block is 4096 pointers. Allocating one in the constructor charged every
// queue 32 KB whether or not anything was ever pushed, and the transaction
// pool builds one per reorg that usually stays empty — 46% of a node's total
// allocation under load. These tests pin the deferral and the behaviour it
// must not change.

package prque

import (
	"testing"
)

// TestEmptyQueueAllocatesNoBlock is the regression: constructing a queue and
// leaving it empty must not allocate a block.
func TestEmptyQueueAllocatesNoBlock(t *testing.T) {
	allocs := testing.AllocsPerRun(50, func() {
		q := New(nil)
		if q.Size() != 0 {
			t.Fatal("fresh queue is not empty")
		}
	})
	// The Prque and sstack headers remain; the 4096-entry block must not.
	if allocs > 3 {
		t.Fatalf("empty queue cost %.0f allocations, want the block deferred", allocs)
	}
	t.Logf("allocations for an unused queue: %.0f", allocs)
}

// TestPushAllocatesOnDemand: the first element brings the block with it, and
// the queue behaves exactly as before.
func TestPushAllocatesOnDemand(t *testing.T) {
	q := New(nil)
	if q.Size() != 0 || !q.Empty() {
		t.Fatal("fresh queue should be empty")
	}
	q.Push("a", 1)
	if q.Size() != 1 {
		t.Fatalf("size after one push = %d", q.Size())
	}
	v, p := q.Peek()
	if v != "a" || p != 1 {
		t.Fatalf("Peek = (%v,%d), want (a,1)", v, p)
	}
	v, p = q.Pop()
	if v != "a" || p != 1 {
		t.Fatalf("Pop = (%v,%d), want (a,1)", v, p)
	}
	if !q.Empty() {
		t.Fatal("queue should be empty after popping its only element")
	}
}

// TestPeekOnEmptyReportsEmpty: Peek used to dereference a nil element and
// panic; with the block deferred it would have indexed past the end.
func TestPeekOnEmptyReportsEmpty(t *testing.T) {
	q := New(nil)
	v, p := q.Peek()
	if v != nil || p != 0 {
		t.Fatalf("Peek on empty = (%v,%d), want (nil,0)", v, p)
	}
}

// TestOrderingAcrossBlockBoundary pushes well past one block so the growth
// path — the one that now also allocates the first block — is exercised in
// both directions.
func TestOrderingAcrossBlockBoundary(t *testing.T) {
	const n = blockSize + 500
	q := New(nil)
	for i := 0; i < n; i++ {
		q.Push(i, int64(i))
	}
	if q.Size() != n {
		t.Fatalf("size = %d, want %d", q.Size(), n)
	}
	for i := n - 1; i >= 0; i-- {
		v, p := q.Pop()
		if v.(int) != i || p != int64(i) {
			t.Fatalf("Pop = (%v,%d), want (%d,%d)", v, p, i, i)
		}
	}
	if !q.Empty() {
		t.Fatal("queue should be empty")
	}
}

// TestResetLeavesNoBlock: Reset rebuilds the stack, so it must inherit the
// deferral rather than quietly re-allocating.
func TestResetLeavesNoBlock(t *testing.T) {
	q := New(nil)
	q.Push("a", 1)
	q.Reset()
	if !q.Empty() {
		t.Fatal("Reset did not empty the queue")
	}
	if q.cont.capacity != 0 {
		t.Fatalf("Reset left capacity %d, want 0", q.cont.capacity)
	}
	q.Push("b", 2)
	if v, _ := q.Peek(); v != "b" {
		t.Fatalf("queue unusable after Reset: Peek = %v", v)
	}
}
