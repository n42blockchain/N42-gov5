// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import "testing"

func TestMessageSizeClass(t *testing.T) {
	cases := []struct {
		size int
		want int
	}{
		{0, 0},
		{1, 0},
		{256, 0},
		{257, 1},
		{512, 1},
		{513, 2},
		{8 << 20, 15}, // largest class: 256 << 15 == 8 MiB
	}
	for _, c := range cases {
		if got := messageSizeClass(c.size); got != c.want {
			t.Errorf("messageSizeClass(%d) = %d, want %d", c.size, got, c.want)
		}
	}
	// Anything bigger than the largest pool class is rejected.
	if got := messageSizeClass(8<<20 + 1); got != -1 {
		t.Errorf("messageSizeClass(8MiB+1) = %d, want -1", got)
	}
	if got := messageSizeClass(1 << 30); got != -1 {
		t.Errorf("messageSizeClass(1GiB) = %d, want -1", got)
	}
}

func TestGetPutMessageBufferRoundTrip(t *testing.T) {
	buf := GetMessageBuffer(100)
	if len(buf) != 100 {
		t.Fatalf("len(buf) = %d, want 100", len(buf))
	}
	PutMessageBuffer(buf)

	// A second Get of a pooled size class should not panic and returns a
	// buffer of the requested length (whether reused or freshly made).
	buf2 := GetMessageBuffer(100)
	if len(buf2) != 100 {
		t.Fatalf("len(buf2) = %d, want 100", len(buf2))
	}
}

func TestGetMessageBufferOversized(t *testing.T) {
	// Larger than the largest pool class: falls back to make([]byte, size).
	size := 8<<20 + 1
	buf := GetMessageBuffer(size)
	if len(buf) != size {
		t.Fatalf("len(buf) = %d, want %d", len(buf), size)
	}
	// Putting an oversized (non-power-of-class-size) buffer back is a no-op,
	// not a panic.
	PutMessageBuffer(buf)
}

func TestPutMessageBufferIgnoresWrongCapacity(t *testing.T) {
	// A buffer whose capacity doesn't exactly match its class's expected
	// size must not be pooled (it would corrupt the size invariant).
	odd := make([]byte, 300, 300)
	PutMessageBuffer(odd) // should not panic
}

func TestPeerMessageQueueEnqueueDequeue(t *testing.T) {
	q := NewPeerMessageQueue(2)

	if !q.Enqueue([]byte("a")) {
		t.Fatal("Enqueue should succeed under capacity")
	}
	if !q.Enqueue([]byte("b")) {
		t.Fatal("Enqueue should succeed at capacity boundary")
	}
	if q.Enqueue([]byte("c")) {
		t.Fatal("Enqueue should fail once the queue is full")
	}
	if got := q.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}

	if got := string(q.Dequeue()); got != "a" {
		t.Fatalf("Dequeue() = %q, want %q (FIFO order)", got, "a")
	}
	if got := string(q.Dequeue()); got != "b" {
		t.Fatalf("Dequeue() = %q, want %q", got, "b")
	}
	if got := q.Dequeue(); got != nil {
		t.Fatalf("Dequeue() on empty queue = %v, want nil", got)
	}
	if got := q.Len(); got != 0 {
		t.Fatalf("Len() after draining = %d, want 0", got)
	}
}

func TestPeerMessageQueueClear(t *testing.T) {
	q := NewPeerMessageQueue(4)
	q.Enqueue([]byte("x"))
	q.Enqueue([]byte("y"))

	msgs := q.Clear()
	if len(msgs) != 2 {
		t.Fatalf("Clear() returned %d messages, want 2", len(msgs))
	}
	if got := q.Len(); got != 0 {
		t.Fatalf("Len() after Clear = %d, want 0", got)
	}
}

func TestBatchSendAddAndFlush(t *testing.T) {
	b := NewBatchSend(2)

	if !b.Add([]byte("ab")) {
		t.Fatal("Add should succeed under capacity")
	}
	if !b.Add([]byte("cde")) {
		t.Fatal("Add should succeed at capacity boundary")
	}
	if b.Add([]byte("f")) {
		t.Fatal("Add should fail once the batch is full")
	}
	if got := b.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}
	if got := b.TotalLen(); got != 5 {
		t.Fatalf("TotalLen() = %d, want 5", got)
	}

	msgs, total := b.Flush()
	if len(msgs) != 2 || total != 5 {
		t.Fatalf("Flush() = (%v, %d), want (len 2, 5)", msgs, total)
	}
	if got := b.Len(); got != 0 {
		t.Fatalf("Len() after Flush = %d, want 0", got)
	}
	if got := b.TotalLen(); got != 0 {
		t.Fatalf("TotalLen() after Flush = %d, want 0", got)
	}

	// The batch is reusable after a flush.
	if !b.Add([]byte("z")) {
		t.Fatal("Add after Flush should succeed")
	}
}
