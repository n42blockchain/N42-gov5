package qmdb

import "testing"

// entryRows counts physical rows in the store's entry table.
func entryRows(store mapStore) int { return len(store[EntryTable]) }

// TestDeadEntryPruning: the on-disk entry log must track the LIVE set, not the
// full append history. Dead-at-flush slots are never written; slots that die
// after being flushed are deleted at the next flush. Both reload paths (full
// rebuild and persistent-index fast path) must still reproduce the exact root
// and values.
func TestDeadEntryPruning(t *testing.T) {
	store := newMapStore()
	shared := persistMap{}
	tr := New()
	tr.SetIndex(&countingIndex{m: shared})
	tr.SetCold(ColdReaderFromGetter(store))
	tr.SetLeafStore(LeafStoreFromGetter(store))

	const n = 6000 // ~3 twigs
	for i := uint64(0); i < n; i++ {
		tr.Set(key(i), val(i))
	}
	// Overwrite a third BEFORE the first flush -> their old slots are dead at
	// flush time and must never hit disk.
	for i := uint64(0); i < n; i += 3 {
		tr.Set(key(i), val(i+1))
	}
	next, _, err := tr.FlushTo(store, 0)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	rowsAfterFirst := entryRows(store)
	if rowsAfterFirst != tr.LiveCount() {
		t.Fatalf("first flush should persist exactly the live set: rows=%d live=%d", rowsAfterFirst, tr.LiveCount())
	}
	tr.EvictThrough(next)
	tr.EvictTwigsThrough(next)

	// Now kill flushed rows: overwrite some cold keys and delete others.
	for i := uint64(1); i < n; i += 5 {
		tr.Set(key(i), val(i+7)) // old (flushed) slot dies -> scheduled deletion
	}
	for i := uint64(2); i < n; i += 7 {
		tr.Delete(key(i))
	}
	if len(tr.deadFlushed) == 0 {
		t.Fatal("expected dead flushed slots to be scheduled")
	}
	next2, _, err := tr.FlushTo(store, next)
	if err != nil {
		t.Fatalf("flush2: %v", err)
	}
	if len(tr.deadFlushed) != 0 {
		t.Fatal("dead list not drained")
	}
	if rows := entryRows(store); rows != tr.LiveCount() {
		t.Fatalf("after pruning, rows must equal live set: rows=%d live=%d", rows, tr.LiveCount())
	}
	tr.EvictThrough(next2)
	tr.EvictTwigsThrough(next2)
	want := tr.Root()
	wantLive := tr.LiveCount()

	// Reload path 1: full rebuild (fresh in-RAM index).
	rebuilt := New()
	if err := rebuilt.LoadFrom(store); err != nil {
		t.Fatalf("rebuild load: %v", err)
	}
	if rebuilt.Root() != want || rebuilt.LiveCount() != wantLive {
		t.Fatalf("rebuild reload diverged: root %x vs %x, live %d vs %d",
			rebuilt.Root(), want, rebuilt.LiveCount(), wantLive)
	}
	// Reload path 2: persistent-index fast path.
	fast := New()
	fast.SetIndex(&countingIndex{m: shared})
	if err := fast.LoadFrom(store); err != nil {
		t.Fatalf("fast load: %v", err)
	}
	if fast.Root() != want || fast.LiveCount() != wantLive {
		t.Fatalf("fast-path reload diverged")
	}
	// Live values still resolve (cold faults read live rows only) + proofs hold.
	fast.SetCold(ColdReaderFromGetter(store))
	fast.SetLeafStore(LeafStoreFromGetter(store))
	for i := uint64(0); i < n; i += 11 {
		gw, ok1 := tr.Get(key(i))
		gf, ok2 := fast.Get(key(i))
		if ok1 != ok2 || (ok1 && string(gw) != string(gf)) {
			t.Fatalf("key %d diverged after pruning reload", i)
		}
		if ok2 {
			p, _ := fast.GetProof(key(i))
			if !VerifyProof(want, p) {
				t.Fatalf("key %d proof failed after pruning", i)
			}
		}
	}
}
