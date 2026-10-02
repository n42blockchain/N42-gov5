// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// leafseg_heap_cursor_test.go covers two small leftover methods that satisfy
// standard-library interfaces but are not exercised by the normal merge path
// (writeSorted builds a mergeHeap by direct append + heap.Init, never
// heap.Push) or by any cursor consumer that calls Close explicitly:
// mergeHeap.Push (container/heap.Interface) and segLeafCursor.Close (the
// kv.Cursor-shaped no-op close).
package datc

import "testing"

func TestMergeHeapPush(t *testing.T) {
	h := &mergeHeap{}
	item := mergeItem{src: &segMergeSource{}, prio: 3}
	h.Push(item)
	if h.Len() != 1 {
		t.Fatalf("expected one item after Push, got %d", h.Len())
	}
	if (*h)[0].prio != 3 {
		t.Fatalf("expected the pushed item to be stored, got prio=%d", (*h)[0].prio)
	}
}

func TestSegLeafCursorClose(t *testing.T) {
	f := newRunFixture(t)
	q, tx, cleanup := f.openQuerier(t)
	defer cleanup()
	_ = q
	_ = tx
	set, ok, err := openLeafSegSet(f.archiveDir, leafTableS, newFrameLRU())
	if err != nil || !ok {
		t.Fatalf("openLeafSegSet: ok=%v err=%v", ok, err)
	}
	defer set.Close()
	c := set.Cursor()
	// Close is a documented no-op (the underlying segment files are owned
	// and released by the set, not the cursor); calling it must not panic.
	c.Close()
}
