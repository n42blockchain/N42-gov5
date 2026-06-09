package qmdb

import "testing"

// flushAndEvict mirrors the engine's per-batch maintenance: persist new entries
// to the cold store, then drop them from RAM. flushedThrough is carried by the
// caller (the test) the way the root computer carries it.
func flushAndEvict(t *testing.T, tr *Tree, store mapStore, flushedThrough uint64) uint64 {
	t.Helper()
	next, _, err := tr.FlushTo(store, flushedThrough)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	tr.EvictThrough(next)
	return next
}

// TestEvictionRootEquivalence: an evicting tree (entries faulted from a cold
// store) must produce the SAME world root and the SAME live values as a plain
// all-in-RAM tree, at every checkpoint, across set/update/delete churn that
// spans many twigs. This is the core correctness guarantee of the cold tier.
func TestEvictionRootEquivalence(t *testing.T) {
	store := newMapStore()
	evicting := New()
	evicting.SetCold(ColdReaderFromGetter(store))
	plain := New()

	rng := uint64(0x243F6A8885A308D3)
	next := func() uint64 { rng = rng*6364136223846793005 + 1442695040888963407; return rng >> 11 }

	const keys = 4000
	var flushed uint64
	for round := 0; round < 60; round++ {
		for op := 0; op < 200; op++ {
			k := next() % keys
			if next()%5 == 0 {
				evicting.Delete(key(k))
				plain.Delete(key(k))
			} else {
				v := val(next())
				evicting.Set(key(k), v)
				plain.Set(key(k), v)
			}
		}
		// flush+evict the evicting tree every few rounds
		if round%3 == 2 {
			flushed = flushAndEvict(t, evicting, store, flushed)
		}
		if evicting.Root() != plain.Root() {
			t.Fatalf("round %d: evicting root %x != plain root %x", round, evicting.Root(), plain.Root())
		}
	}
	// Final live-set value equivalence.
	for i := uint64(0); i < keys; i++ {
		ev, eok := evicting.Get(key(i))
		pv, pok := plain.Get(key(i))
		if eok != pok {
			t.Fatalf("key %d liveness: evicting=%v plain=%v", i, eok, pok)
		}
		if eok && string(ev) != string(pv) {
			t.Fatalf("key %d value diverged after eviction", i)
		}
	}
}

// TestEvictionBoundsResident: as history grows, the resident entry window must
// stay bounded (tracking only the unflushed tail), NOT grow with nextSlot. This
// is the memory capability — the whole point of the cold tier.
func TestEvictionBoundsResident(t *testing.T) {
	store := newMapStore()
	tr := New()
	tr.SetCold(ColdReaderFromGetter(store))

	const keys = 3000
	var flushed uint64
	maxResident := 0
	for round := uint64(0); round < 200; round++ {
		for op := uint64(0); op < 300; op++ {
			k := (round*300 + op) % keys
			tr.Set(key(k), val(round*1000+k))
		}
		flushed = flushAndEvict(t, tr, store, flushed)
		if r := tr.ResidentEntries(); r > maxResident {
			maxResident = r
		}
	}
	total := tr.NextSlot()
	t.Logf("nextSlot=%d evicted=%d residentMax=%d residentNow=%d", total, total-uint64(tr.ResidentEntries()), maxResident, tr.ResidentEntries())
	if total < 50000 {
		t.Fatalf("test did not generate enough history: nextSlot=%d", total)
	}
	// Resident window must be a small fraction of total history. After a flush+
	// evict the window collapses to ~0; even the peak stays bounded by one
	// round's appends, never O(history).
	if uint64(maxResident) > total/4 {
		t.Fatalf("resident window not bounded: max=%d total=%d", maxResident, total)
	}
	if tr.ResidentEntries() > 1 {
		t.Fatalf("after final flush+evict, window should be ~empty, got %d", tr.ResidentEntries())
	}
}

// TestGetProofAfterEviction: live keys whose entry records have been evicted must
// still resolve (Get) and prove (GetProof) — the value is faulted from cold and
// the leaf/path come from the resident twig.
func TestGetProofAfterEviction(t *testing.T) {
	store := newMapStore()
	tr := New()
	tr.SetCold(ColdReaderFromGetter(store))

	const n = 6000 // > 2 twigs
	for i := uint64(0); i < n; i++ {
		tr.Set(key(i), val(i))
	}
	// flush + evict EVERYTHING, so every live entry is cold.
	next, _, err := tr.FlushTo(store, 0)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	tr.EvictThrough(next)
	if tr.ResidentEntries() != 0 {
		t.Fatalf("expected fully evicted window, got %d resident", tr.ResidentEntries())
	}
	root := tr.Root()
	checked := 0
	for i := uint64(0); i < n; i += 7 {
		v, ok := tr.Get(key(i)) // value served from cold
		if !ok || string(v) != string(val(i)) {
			t.Fatalf("key %d Get failed after full eviction", i)
		}
		p, pok := tr.GetProof(key(i))
		if !pok || !VerifyProof(root, p) {
			t.Fatalf("key %d proof failed after full eviction", i)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no keys checked")
	}
}

// TestDeactivateEvictedSlot: updating/deleting a key whose previous slot is cold
// must correctly deactivate it (null the twig leaf) and yield the same root as a
// plain tree — proving deactivation needs no resident entry record.
func TestDeactivateEvictedSlot(t *testing.T) {
	store := newMapStore()
	ev := New()
	ev.SetCold(ColdReaderFromGetter(store))
	plain := New()

	const n = 5000
	for i := uint64(0); i < n; i++ {
		ev.Set(key(i), val(i))
		plain.Set(key(i), val(i))
	}
	// Evict all current entries → every key's live slot is now cold.
	next, _, _ := ev.FlushTo(store, 0)
	ev.EvictThrough(next)

	// Now update half (append new slot + deactivate the cold old slot) and delete
	// a quarter (deactivate cold slot, no new entry).
	for i := uint64(0); i < n; i += 2 {
		ev.Set(key(i), val(i+777))
		plain.Set(key(i), val(i+777))
	}
	for i := uint64(1); i < n; i += 4 {
		ev.Delete(key(i))
		plain.Delete(key(i))
	}
	if ev.Root() != plain.Root() {
		t.Fatalf("root diverged after deactivating cold slots: ev=%x plain=%x", ev.Root(), plain.Root())
	}
	if ev.LiveCount() != plain.LiveCount() {
		t.Fatalf("live count diverged: ev=%d plain=%d", ev.LiveCount(), plain.LiveCount())
	}
}

// TestCompactWithEviction: compaction must work when the live entries it relocates
// live in cold storage (faulted via the cold reader), preserving the live set.
func TestCompactWithEviction(t *testing.T) {
	store := newMapStore()
	tr := New()
	tr.SetCold(ColdReaderFromGetter(store))

	const n = 6000
	for i := uint64(0); i < n; i++ {
		tr.Set(key(i), val(i))
	}
	// Delete most of the early twigs to make them sparse-but-live.
	for i := uint64(0); i < TwigSize-20; i++ {
		tr.Delete(key(i))
	}
	// Flush + evict so the surviving live entries in sparse twigs are cold.
	next, _, _ := tr.FlushTo(store, 0)
	tr.EvictThrough(next)

	liveBefore := tr.LiveCount()
	pruned := tr.Compact(0.25) // relocates cold live entries to the active twig
	if pruned == 0 {
		t.Fatal("expected compaction to prune sparse twigs")
	}
	if tr.LiveCount() != liveBefore {
		t.Fatalf("compaction changed live set: %d -> %d", liveBefore, tr.LiveCount())
	}
	root := tr.Root()
	for i := uint64(TwigSize - 20); i < n; i += 13 {
		v, ok := tr.Get(key(i))
		if !ok || string(v) != string(val(i)) {
			t.Fatalf("key %d lost/changed after compact+eviction", i)
		}
		p, _ := tr.GetProof(key(i))
		if !VerifyProof(root, p) {
			t.Fatalf("key %d proof failed after compact+eviction", i)
		}
	}
}
