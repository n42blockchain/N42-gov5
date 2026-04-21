// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"bytes"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestMVHashMap_EmptyRead(t *testing.T) {
	m := NewMVHashMap(16)
	val, ver, status := m.Read([]byte("key"), 0)
	if status != MVNotFound {
		t.Fatalf("empty map should return MVNotFound, got status=%d val=%x ver=%+v", status, val, ver)
	}
}

func TestMVHashMap_SingleWriteRead(t *testing.T) {
	m := NewMVHashMap(16)
	key := []byte("slotX")

	// Tx 5 writes V0.
	m.Write(key, Version{TxIdx: 5, Incarnation: 0}, []byte("V0"))

	// Tx 3 (older) reads: no write visible (5 > 3).
	_, _, status := m.Read(key, 3)
	if status != MVNotFound {
		t.Fatalf("reader=3, writer=5: expected NotFound, got %d", status)
	}

	// Tx 5 itself reads: NOT visible (self-read); reader sees txIdx < 5.
	_, _, status = m.Read(key, 5)
	if status != MVNotFound {
		t.Fatalf("reader=5, writer=5: expected NotFound (self-read excluded), got %d", status)
	}

	// Tx 6 reads: sees tx 5's write.
	val, ver, status := m.Read(key, 6)
	if status != MVOk {
		t.Fatalf("reader=6: expected Ok, got %d", status)
	}
	if !bytes.Equal(val, []byte("V0")) {
		t.Fatalf("val: got %x want V0", val)
	}
	if ver.TxIdx != 5 || ver.Incarnation != 0 {
		t.Fatalf("version: got %+v want {5,0}", ver)
	}
}

func TestMVHashMap_MultipleWriters(t *testing.T) {
	m := NewMVHashMap(16)
	key := []byte("slotY")

	// Writes at tx 2, 5, 8.
	m.Write(key, Version{TxIdx: 2, Incarnation: 0}, []byte("V2"))
	m.Write(key, Version{TxIdx: 5, Incarnation: 0}, []byte("V5"))
	m.Write(key, Version{TxIdx: 8, Incarnation: 0}, []byte("V8"))

	cases := []struct {
		reader  int
		wantVal string
		wantIdx int
		wantSt  ReadStatus
	}{
		{0, "", 0, MVNotFound},   // nothing before tx 0
		{2, "", 0, MVNotFound},   // self-read excluded
		{3, "V2", 2, MVOk},       // sees tx 2
		{5, "V2", 2, MVOk},       // self-read of 5 excluded, falls to 2
		{6, "V5", 5, MVOk},       // sees tx 5
		{8, "V5", 5, MVOk},       // self-read of 8 excluded, falls to 5
		{9, "V8", 8, MVOk},       // sees tx 8
		{100, "V8", 8, MVOk},     // same
	}
	for _, c := range cases {
		val, ver, st := m.Read(key, c.reader)
		if st != c.wantSt {
			t.Errorf("reader=%d: status=%d want %d", c.reader, st, c.wantSt)
			continue
		}
		if st == MVOk {
			if string(val) != c.wantVal {
				t.Errorf("reader=%d: val=%s want %s", c.reader, val, c.wantVal)
			}
			if ver.TxIdx != c.wantIdx {
				t.Errorf("reader=%d: writer=%d want %d", c.reader, ver.TxIdx, c.wantIdx)
			}
		}
	}
}

func TestMVHashMap_IncarnationOverwrite(t *testing.T) {
	m := NewMVHashMap(16)
	key := []byte("k")

	m.Write(key, Version{TxIdx: 5, Incarnation: 0}, []byte("V_inc0"))
	// Re-execution of tx 5 bumps incarnation.
	m.Write(key, Version{TxIdx: 5, Incarnation: 1}, []byte("V_inc1"))

	val, ver, st := m.Read(key, 6)
	if st != MVOk {
		t.Fatalf("status=%d want Ok", st)
	}
	if string(val) != "V_inc1" {
		t.Errorf("val=%s want V_inc1", val)
	}
	if ver.Incarnation != 1 {
		t.Errorf("inc=%d want 1", ver.Incarnation)
	}
}

func TestMVHashMap_Delete(t *testing.T) {
	m := NewMVHashMap(16)
	key := []byte("k")

	m.Write(key, Version{TxIdx: 3, Incarnation: 0}, []byte("V3"))
	m.Write(key, Version{TxIdx: 7, Incarnation: 0}, []byte("V7"))

	// Before delete: reader@8 sees V7.
	val, _, st := m.Read(key, 8)
	if st != MVOk || string(val) != "V7" {
		t.Fatalf("before delete: got st=%d val=%s", st, val)
	}

	// Delete tx 7's write.
	m.Delete(key, 7)

	// After delete: reader@8 falls back to V3.
	val, ver, st := m.Read(key, 8)
	if st != MVOk || string(val) != "V3" || ver.TxIdx != 3 {
		t.Fatalf("after delete: got st=%d val=%s ver=%+v", st, val, ver)
	}
}

func TestMVHashMap_MarkEstimate(t *testing.T) {
	m := NewMVHashMap(16)
	key := []byte("k")

	m.Write(key, Version{TxIdx: 5, Incarnation: 0}, []byte("V5"))

	// Before: reader@6 sees Ok.
	_, _, st := m.Read(key, 6)
	if st != MVOk {
		t.Fatalf("before mark: st=%d want Ok", st)
	}

	// Mark tx 5's write as estimate.
	m.MarkEstimate(key, 5)

	// After: reader@6 sees Estimate.
	_, ver, st := m.Read(key, 6)
	if st != MVEstimate {
		t.Fatalf("after mark: st=%d want Estimate", st)
	}
	if ver.TxIdx != 5 {
		t.Errorf("blocking writer: got %d want 5", ver.TxIdx)
	}
}

func TestMVHashMap_EstimateFallsThroughToOlder(t *testing.T) {
	// When the nearest writer is estimate but a commit exists further
	// back, the read STILL returns Estimate (reader waits on the
	// pending writer rather than using stale committed data). This is
	// the Block-STM "wait for closer write" invariant.
	m := NewMVHashMap(16)
	key := []byte("k")

	m.Write(key, Version{TxIdx: 2, Incarnation: 0}, []byte("V2"))
	m.Write(key, Version{TxIdx: 7, Incarnation: 0}, []byte("V7"))
	m.MarkEstimate(key, 7)

	_, ver, st := m.Read(key, 10)
	if st != MVEstimate {
		t.Fatalf("expected Estimate (newer write is pending); got st=%d", st)
	}
	if ver.TxIdx != 7 {
		t.Errorf("blocking writer: got %d want 7", ver.TxIdx)
	}

	// Reader before the estimate still sees older commit.
	val, ver, st := m.Read(key, 5)
	if st != MVOk || string(val) != "V2" || ver.TxIdx != 2 {
		t.Fatalf("reader@5: got st=%d val=%s ver=%+v", st, val, ver)
	}
}

func TestMVHashMap_CollectHighestWrites(t *testing.T) {
	m := NewMVHashMap(16)
	m.Write([]byte("a"), Version{TxIdx: 1}, []byte("a1"))
	m.Write([]byte("a"), Version{TxIdx: 5}, []byte("a5"))
	m.Write([]byte("b"), Version{TxIdx: 2}, []byte("b2"))
	m.Write([]byte("b"), Version{TxIdx: 3}, []byte("b3"))
	m.Write([]byte("c"), Version{TxIdx: 10}, []byte("c10"))

	got := m.CollectHighestWrites()
	if len(got) != 3 {
		t.Fatalf("got %d entries; want 3", len(got))
	}
	// Map into lookup for order-independent check.
	final := make(map[string]CommitEntry, 3)
	for _, e := range got {
		final[string(e.Key)] = e
	}
	if string(final["a"].Value) != "a5" || final["a"].TxIdx != 5 {
		t.Errorf("a: got %+v", final["a"])
	}
	if string(final["b"].Value) != "b3" || final["b"].TxIdx != 3 {
		t.Errorf("b: got %+v", final["b"])
	}
	if string(final["c"].Value) != "c10" || final["c"].TxIdx != 10 {
		t.Errorf("c: got %+v", final["c"])
	}
}

func TestMVHashMap_ConcurrentWritesDifferentKeys(t *testing.T) {
	m := NewMVHashMap(128)
	const (
		numKeys = 1000
		numTxs  = 100
	)
	var wg sync.WaitGroup
	wg.Add(numTxs)
	for txi := 0; txi < numTxs; txi++ {
		go func(txIdx int) {
			defer wg.Done()
			for k := 0; k < numKeys; k++ {
				key := []byte(fmt.Sprintf("key-%d", k))
				val := []byte(fmt.Sprintf("tx%d-v", txIdx))
				m.Write(key, Version{TxIdx: txIdx}, val)
			}
		}(txi)
	}
	wg.Wait()

	// Verify: each key has entries from all txs; highest-txIdx read
	// returns the last tx's value.
	for k := 0; k < numKeys; k++ {
		key := []byte(fmt.Sprintf("key-%d", k))
		val, ver, st := m.Read(key, numTxs+1)
		if st != MVOk {
			t.Fatalf("key %d: st=%d", k, st)
		}
		wantVal := fmt.Sprintf("tx%d-v", numTxs-1)
		if string(val) != wantVal {
			t.Errorf("key %d: val=%s want %s", k, val, wantVal)
		}
		if ver.TxIdx != numTxs-1 {
			t.Errorf("key %d: writer=%d want %d", k, ver.TxIdx, numTxs-1)
		}
	}
}

func TestMVHashMap_ConcurrentReadWriteSameKey(t *testing.T) {
	// Stress: 16 writers (each at a distinct txIdx) and 16 readers at
	// random txIdx hammer the same key. No panics, reads are always
	// consistent with some snapshot.
	m := NewMVHashMap(16)
	key := []byte("hotkey")
	const iterations = 1000
	var readsOk atomic.Int64
	var estimates atomic.Int64

	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(writerIdx int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				m.Write(key, Version{TxIdx: writerIdx, Incarnation: uint32(i)},
					[]byte(fmt.Sprintf("w%d-i%d", writerIdx, i)))
			}
		}(w)
	}
	for r := 0; r < 16; r++ {
		wg.Add(1)
		go func(readerIdx int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_, _, st := m.Read(key, 20)
				switch st {
				case MVOk:
					readsOk.Add(1)
				case MVEstimate:
					estimates.Add(1)
				}
			}
		}(r)
	}
	wg.Wait()

	if readsOk.Load()+estimates.Load() < int64(16*iterations)/2 {
		t.Errorf("most reads should succeed; got ok=%d estimate=%d",
			readsOk.Load(), estimates.Load())
	}
}

func TestMVHashMap_Sharding(t *testing.T) {
	// Confirms shard count is rounded up to power of 2, mask is valid.
	m := NewMVHashMap(5) // requests 5 -> should get 8
	if len(m.shards) != 8 {
		t.Errorf("shards=%d want 8", len(m.shards))
	}
	if m.mask != 7 {
		t.Errorf("mask=%d want 7", m.mask)
	}
}

func BenchmarkMVHashMap_Read(b *testing.B) {
	m := NewMVHashMap(64)
	key := []byte("bench-key")
	for i := 0; i < 100; i++ {
		m.Write(key, Version{TxIdx: i}, []byte{byte(i)})
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _, _ = m.Read(key, 50)
		}
	})
}

func BenchmarkMVHashMap_Write(b *testing.B) {
	m := NewMVHashMap(64)
	keys := make([][]byte, 1024)
	for i := range keys {
		keys[i] = []byte(fmt.Sprintf("k-%04d", i))
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			m.Write(keys[i&1023], Version{TxIdx: i}, []byte{byte(i)})
			i++
		}
	})
}
