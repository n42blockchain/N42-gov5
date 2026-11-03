package qmdb

import (
	"bytes"
	"fmt"
	"slices"
	"testing"
)

// A candidate may be peeled after earlier (unrelated) dead rows were queued
// for reclamation. Only its revivals must be removed from that queue.
func TestApplyUndoPreservesUnrelatedReclamation(t *testing.T) {
	store := newMapStore()
	tr := New()
	tr.SetCold(ColdReaderFromGetter(store))
	tr.SetLeafStore(LeafStoreFromGetter(store))
	const count = 10000
	for i := uint64(0); i < count; i++ {
		tr.Set(key(i), rvVal("base", i))
	}
	flushed := flushAndEvict(t, tr, store, 0)
	for i := uint64(0); i < count; i += 5 {
		tr.Delete(key(i))
	}
	wantDead := slices.Clone(tr.deadFlushed)
	wantRoot := tr.Root()
	tr.StartUndoRecording()
	tr.BeginLeafBatch()
	for i := uint64(0); i < count; i++ {
		if i%5 != 0 {
			tr.Set(key(i), []byte("candidate"))
			if i%2 == 0 {
				tr.Delete(key(i)) // also deactivates an in-block append
			}
		}
	}
	tr.EndLeafBatch()
	undo := tr.StopUndoRecording()
	if err := tr.ApplyUndo(undo); err != nil {
		t.Fatal(err)
	}
	if tr.Root() != wantRoot {
		t.Fatal("revert changed the pre-candidate root")
	}
	gotDead := slices.Clone(tr.deadFlushed)
	slices.Sort(wantDead)
	slices.Sort(gotDead)
	if !slices.Equal(wantDead, gotDead) {
		t.Fatal("revert removed unrelated dead rows or retained revived rows")
	}
	flushAndEvict(t, tr, store, flushed)
	for i := uint64(0); i < count; i++ {
		value, ok := tr.Get(key(i))
		if i%5 == 0 {
			if ok {
				t.Fatalf("earlier deletion %d was revived", i)
			}
			if row, err := store.GetOne(EntryTable, be8(i)); err != nil || len(row) != 0 {
				t.Fatalf("earlier dead row %d was not reclaimed: %v", i, err)
			}
		} else if !ok || !bytes.Equal(value, rvVal("base", i)) {
			t.Fatalf("revived value %d was lost after flush", i)
		}
	}
}

// Measures the live candidate-peel shape: an already-flushed state followed
// by many overwrites. Setup is excluded; ApplyUndo and the restored root are
// timed. The queue and undo record grow together with the block size.
func BenchmarkApplyUndoReclamation(b *testing.B) {
	for _, count := range []int{20000, 163000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			b.ReportAllocs()
			for n := 0; n < b.N; n++ {
				b.StopTimer()
				tr := New()
				store := newMapStore()
				tr.SetCold(ColdReaderFromGetter(store))
				tr.SetLeafStore(LeafStoreFromGetter(store))
				tr.BeginLeafBatch()
				for i := 0; i < count; i++ {
					tr.Set(key(uint64(i)), []byte("base"))
				}
				tr.EndLeafBatch()
				root := tr.Root()
				flushed, _, err := tr.FlushTo(store, 0)
				if err != nil {
					b.Fatal(err)
				}
				tr.CommitFlush()
				tr.EvictThrough(flushed)
				tr.StartUndoRecording()
				tr.BeginLeafBatch()
				for i := 0; i < count; i++ {
					tr.Set(key(uint64(i)), []byte("candidate"))
				}
				tr.EndLeafBatch()
				undo := tr.StopUndoRecording()
				tr.Root()
				if len(tr.deadFlushed) != count {
					b.Fatalf("queue size %d, want %d", len(tr.deadFlushed), count)
				}
				b.StartTimer()
				if err := tr.ApplyUndo(undo); err != nil {
					b.Fatal(err)
				}
				if tr.Root() != root {
					b.Fatal("wrong restored root")
				}
			}
		})
	}
}
