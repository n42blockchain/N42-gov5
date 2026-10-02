// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import "testing"

// TestG52S3FIFOEvictMLocked drives evictMLocked directly against a
// hand-built M list: a tail entry with freq==0 (byte eviction straight to
// ghost) and, ahead of it, an entry with freq>0 (the second-chance demote:
// freq decremented and moved to the front instead of being evicted). One
// call must demote the hot entry and evict the cold one, since the cold
// entry becomes the new tail after the demote's listRemove/listPushFront.
// No existing test reaches this function -- real workloads only hit it
// under sustained capacity pressure that unit tests don't reproduce.
func TestG52S3FIFOEvictMLocked(t *testing.T) {
	c := newS3FIFO[int](1<<20, 8)

	hotIdx := c.allocSlot()
	if hotIdx == nilSlot {
		t.Fatal("allocSlot exhausted before test setup")
	}
	c.keys[hotIdx] = 100
	c.items[100] = hotIdx
	c.setFreq(hotIdx, 2)
	c.flags[hotIdx] |= flagInMain
	c.listPushFront(&c.mHead, &c.mTail, hotIdx)
	c.mLen++

	coldIdx := c.allocSlot()
	if coldIdx == nilSlot {
		t.Fatal("allocSlot exhausted before test setup")
	}
	c.keys[coldIdx] = 200
	c.items[200] = coldIdx
	c.setFreq(coldIdx, 0)
	c.flags[coldIdx] |= flagInMain
	c.listPushFront(&c.mHead, &c.mTail, coldIdx)
	c.mLen++

	// coldIdx was pushed last, so it is mHead; hotIdx (pushed first) is
	// mTail -- evictMLocked walks from the tail.
	if c.mTail != hotIdx {
		t.Fatalf("test setup: expected mTail=hotIdx(%d), got %d", hotIdx, c.mTail)
	}

	if evicted := c.evictMLocked(); !evicted {
		t.Fatal("evictMLocked should report an eviction once it reaches the cold entry")
	}

	// The hot entry (freq was 2) must survive with freq decremented, now
	// demoted to the front of M.
	if _, present := c.items[100]; !present {
		t.Fatal("hot entry (freq>0) should survive a second-chance demotion, not be evicted")
	}
	if got := c.freq(hotIdx); got != 1 {
		t.Fatalf("hot entry's freq should decrement from 2 to 1, got %d", got)
	}
	if c.mHead != hotIdx {
		t.Fatalf("demoted hot entry should now be at the front of M, mHead=%d want %d", c.mHead, hotIdx)
	}

	// The cold entry (freq was 0) must be gone: evicted to ghost.
	if _, present := c.items[200]; present {
		t.Fatal("cold entry (freq==0) should have been evicted")
	}
	if c.mLen != 1 {
		t.Fatalf("mLen after one eviction = %d, want 1", c.mLen)
	}

	// An empty M list reports no eviction rather than panicking on a
	// nilSlot tail.
	c2 := newS3FIFO[int](1<<20, 8)
	if c2.evictMLocked() {
		t.Fatal("evictMLocked on an empty M list should return false")
	}
}
