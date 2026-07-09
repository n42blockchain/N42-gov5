package qmdb

import "testing"

// countingGetter wraps a mapStore and counts entry-log reads, so a test can assert
// that leaf-blob rehydration does NOT scan the entry log.
type countingGetter struct {
	g          Getter
	entryReads int
}

func (c *countingGetter) GetOne(table string, key []byte) ([]byte, error) {
	if table == EntryTable {
		c.entryReads++
	}
	return c.g.GetOne(table, key)
}

// TestLeafBlobRehydrationNoEntryScan: with a LeafStore attached, touching an
// evicted twig rebuilds its leaves from the single blob — no per-slot entry reads
// — and yields the same root/proofs as a plain tree.
func TestLeafBlobRehydrationNoEntryScan(t *testing.T) {
	store := newMapStore()
	cg := &countingGetter{g: store}

	ev := New()
	ev.SetCold(ColdReaderFromGetter(cg))
	ev.SetLeafStore(LeafStoreFromGetter(cg))
	plain := New()

	const n = 8000 // ~4 twigs
	for i := uint64(0); i < n; i++ {
		ev.Set(key(i), val(i))
		plain.Set(key(i), val(i))
	}
	// flush writes leaf blobs; then evict sealed twigs.
	next, _, err := ev.FlushTo(store, 0)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	ev.EvictThrough(next)
	ev.EvictTwigsThrough(next)
	if ev.ResidentTwigLeaves() > 2 {
		t.Fatalf("expected sealed twigs evicted, resident=%d", ev.ResidentTwigLeaves())
	}

	cg.entryReads = 0 // measure only the rehydration that follows
	// Update keys spread across the early (evicted) twigs -> forces rehydration.
	for i := uint64(0); i < n; i += 50 {
		ev.Set(key(i), val(i+9))
		plain.Set(key(i), val(i+9))
	}
	if ev.Root() != plain.Root() {
		t.Fatalf("root diverged after leaf-blob rehydration: ev=%x plain=%x", ev.Root(), plain.Root())
	}
	// The whole point: rehydration used blobs, so it must NOT have scanned entries.
	if cg.entryReads != 0 {
		t.Fatalf("leaf-blob rehydration still read %d entries (expected 0)", cg.entryReads)
	}
	// Proofs for evicted twigs still verify (rehydrated from blob).
	root := ev.Root()
	for i := uint64(0); i < n; i += 37 {
		if _, ok := ev.Get(key(i)); !ok {
			continue
		}
		p, _ := ev.GetProof(key(i))
		if !VerifyProof(root, p) {
			t.Fatalf("key %d proof failed after blob rehydration", i)
		}
	}
}

// corruptLeafStore returns a byte-mangled blob for one twig id, to exercise the
// stale/missing-leaf-blob path in ensureHydrated. Set badID to -1 to disable.
type corruptLeafStore struct {
	inner LeafStore
	badID int
}

func (c *corruptLeafStore) Leaves(id int) ([]byte, bool) {
	blob, ok := c.inner.Leaves(id)
	if ok && id == c.badID {
		bad := make([]byte, len(blob))
		copy(bad, blob)
		bad[0] ^= 0xFF // one flipped leaf byte -> recomputed root won't match
		return bad, true
	}
	return blob, ok
}

// TestEnsureHydratedGracefulOnStaleBlob: a sealed, evicted twig whose leaf blob is
// stale/missing must make ensureHydrated RETURN AN ERROR (not panic) and leave the
// twig evicted (nodes still nil, retained root unchanged) so the tree stays
// consistent — the robustness fix that stops one rogue block from panic-crashing a
// node in the branch-switch revert path. A subsequent clean read must recover.
func TestEnsureHydratedGracefulOnStaleBlob(t *testing.T) {
	store := newMapStore()
	tr := New()
	tr.SetCold(ColdReaderFromGetter(store))
	cs := &corruptLeafStore{inner: LeafStoreFromGetter(store), badID: -1}
	tr.SetLeafStore(cs) // attached before flush so leaf blobs get persisted

	const n = 8000 // several twigs
	for i := uint64(0); i < n; i++ {
		tr.Set(key(i), val(i))
	}
	next, _, err := tr.FlushTo(store, 0)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	tr.EvictThrough(next)
	tr.EvictTwigsThrough(next)

	if tr.twigs[0] == nil || tr.twigs[0].nodes != nil || tr.twigs[0].pruned {
		t.Skipf("twig 0 not in the evicted non-pruned state (nodes=%v pruned=%v)",
			tr.twigs[0] != nil && tr.twigs[0].nodes != nil, tr.twigs[0] != nil && tr.twigs[0].pruned)
	}
	wantRoot := tr.twigs[0].root

	cs.badID = 0 // corrupt twig 0's blob
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("ensureHydrated panicked instead of returning an error: %v", r)
			}
		}()
		if err := tr.ensureHydrated(0); err == nil {
			t.Fatal("expected an error for a corrupt leaf blob, got nil")
		}
	}()
	if tr.twigs[0].nodes != nil {
		t.Fatal("failed hydration must leave the twig evicted (nodes still nil)")
	}
	if tr.twigs[0].root != wantRoot {
		t.Fatalf("failed hydration must not alter the retained root: got %x want %x", tr.twigs[0].root, wantRoot)
	}

	// Recovery: a correct blob now hydrates cleanly and commits the node heap.
	cs.badID = -1
	if err := tr.ensureHydrated(0); err != nil {
		t.Fatalf("clean rehydration failed: %v", err)
	}
	if tr.twigs[0].nodes == nil {
		t.Fatal("clean rehydration should have committed the node heap")
	}
	if tr.twigs[0].root != wantRoot {
		t.Fatalf("clean rehydration changed the root: got %x want %x", tr.twigs[0].root, wantRoot)
	}
}

// flushEvictAll mirrors the engine's per-batch maintenance with BOTH tiers:
// persist new entries, then evict entry records AND sealed twig leaves below the
// flushed cursor.
func flushEvictAll(t *testing.T, tr *Tree, store mapStore, flushedThrough uint64) uint64 {
	t.Helper()
	next, _, err := tr.FlushTo(store, flushedThrough)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	tr.EvictThrough(next)
	tr.EvictTwigsThrough(next)
	return next
}

// TestTwigEvictionRootEquivalence: with both entry and twig-leaf eviction active,
// the tree must track a plain all-in-RAM tree exactly across set/update/delete
// churn spanning many twigs — proving rehydration reconstructs identical leaves.
func TestTwigEvictionRootEquivalence(t *testing.T) {
	store := newMapStore()
	ev := New()
	ev.SetCold(ColdReaderFromGetter(store))
	plain := New()

	rng := uint64(0x9E3779B97F4A7C15)
	next := func() uint64 { rng = rng*6364136223846793005 + 1442695040888963407; return rng >> 11 }

	const keys = 5000 // > 2 twigs of live keys
	var flushed uint64
	for round := 0; round < 80; round++ {
		for op := 0; op < 150; op++ {
			k := next() % keys
			if next()%6 == 0 {
				ev.Delete(key(k))
				plain.Delete(key(k))
			} else {
				v := val(next())
				ev.Set(key(k), v)
				plain.Set(key(k), v)
			}
		}
		if round%4 == 3 {
			flushed = flushEvictAll(t, ev, store, flushed)
		}
		if ev.Root() != plain.Root() {
			t.Fatalf("round %d: evicting root %x != plain %x", round, ev.Root(), plain.Root())
		}
	}
	for i := uint64(0); i < keys; i++ {
		ea, eok := ev.Get(key(i))
		pa, pok := plain.Get(key(i))
		if eok != pok || (eok && string(ea) != string(pa)) {
			t.Fatalf("key %d diverged: evicting(ok=%v) plain(ok=%v)", i, eok, pok)
		}
	}
}

// TestTwigEvictionBoundsLeaves: as history grows across many twigs, the number of
// twigs holding their 64 KiB leaf array in RAM must stay bounded (only active +
// recently-touched), NOT grow with the twig count. This is the tier-2 capability.
func TestTwigEvictionBoundsLeaves(t *testing.T) {
	store := newMapStore()
	tr := New()
	tr.SetCold(ColdReaderFromGetter(store))

	// Insert a large, mostly-distinct key set so the append cursor (and twig count)
	// grows steadily; flush+evict each round.
	const perRound = 4000
	var flushed uint64
	maxLeaves := 0
	for round := uint64(0); round < 40; round++ {
		base := round * perRound
		for i := uint64(0); i < perRound; i++ {
			tr.Set(key(base+i), val(base+i))
		}
		flushed = flushEvictAll(t, tr, store, flushed)
		if r := tr.ResidentTwigLeaves(); r > maxLeaves {
			maxLeaves = r
		}
	}
	totalTwigs := len(tr.twigs)
	t.Logf("totalTwigs=%d residentLeavesMax=%d residentLeavesNow=%d", totalTwigs, maxLeaves, tr.ResidentTwigLeaves())
	if totalTwigs < 60 {
		t.Fatalf("not enough twigs to exercise eviction: %d", totalTwigs)
	}
	// Resident leaf arrays must be a tiny constant (the active twig, sometimes its
	// neighbor), never O(total twigs).
	if maxLeaves > 4 {
		t.Fatalf("twig leaves not bounded: max resident=%d of %d twigs", maxLeaves, totalTwigs)
	}
}

// TestProofAfterTwigEviction: a membership proof for a key whose twig has had its
// leaves evicted must still verify — GetProof rehydrates the twig from cold.
func TestProofAfterTwigEviction(t *testing.T) {
	store := newMapStore()
	tr := New()
	tr.SetCold(ColdReaderFromGetter(store))

	const n = 8000 // ~4 twigs
	for i := uint64(0); i < n; i++ {
		tr.Set(key(i), val(i))
	}
	flushEvictAll(t, tr, store, 0) // evict everything below the active twig
	if tr.ResidentTwigLeaves() > 2 {
		t.Fatalf("expected sealed twigs evicted, resident=%d", tr.ResidentTwigLeaves())
	}
	root := tr.Root()
	checked := 0
	for i := uint64(0); i < n; i += 11 {
		p, ok := tr.GetProof(key(i)) // forces twig rehydration
		if !ok || !VerifyProof(root, p) {
			t.Fatalf("key %d proof failed after twig eviction", i)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no keys checked")
	}
}

// TestUpdateRehydratesEvictedTwig: updating/deleting a key whose live slot sits in
// an evicted twig must null that twig's leaf correctly (via rehydration) and yield
// the same root as a plain tree.
func TestUpdateRehydratesEvictedTwig(t *testing.T) {
	store := newMapStore()
	ev := New()
	ev.SetCold(ColdReaderFromGetter(store))
	plain := New()

	const n = 6000
	for i := uint64(0); i < n; i++ {
		ev.Set(key(i), val(i))
		plain.Set(key(i), val(i))
	}
	flushEvictAll(t, ev, store, 0) // seal+evict the early twigs

	// Touch keys spread across the (now-evicted) early twigs.
	for i := uint64(0); i < n; i += 3 {
		ev.Set(key(i), val(i+555)) // update -> deactivate old slot in evicted twig
		plain.Set(key(i), val(i+555))
	}
	for i := uint64(1); i < n; i += 5 {
		ev.Delete(key(i))
		plain.Delete(key(i))
	}
	if ev.Root() != plain.Root() {
		t.Fatalf("root diverged after rehydrating evicted twigs: ev=%x plain=%x", ev.Root(), plain.Root())
	}
	if ev.LiveCount() != plain.LiveCount() {
		t.Fatalf("live count diverged: ev=%d plain=%d", ev.LiveCount(), plain.LiveCount())
	}
}

// TestLoadFromBoundedTwigLeaves: reloading a large persisted store must NOT
// materialize every twig's leaves — only the active twig stays resident, so resume
// memory is bounded.
func TestLoadFromBoundedTwigLeaves(t *testing.T) {
	store := newMapStore()
	src := New()
	src.SetCold(ColdReaderFromGetter(store))
	const n = 12000 // ~6 twigs
	for i := uint64(0); i < n; i++ {
		src.Set(key(i), val(i))
	}
	if _, _, err := src.FlushTo(store, 0); err != nil {
		t.Fatalf("flush: %v", err)
	}
	wantRoot := src.Root()

	dst := New()
	if err := dst.LoadFrom(store); err != nil {
		t.Fatalf("load: %v", err)
	}
	if dst.Root() != wantRoot {
		t.Fatalf("reload root mismatch: want=%x got=%x", wantRoot, dst.Root())
	}
	if dst.ResidentTwigLeaves() > 2 {
		t.Fatalf("LoadFrom kept too many twig leaves resident: %d of %d", dst.ResidentTwigLeaves(), len(dst.twigs))
	}
	// values + proofs still resolve after a bounded reload
	for i := uint64(0); i < n; i += 19 {
		if v, ok := dst.Get(key(i)); !ok || string(v) != string(val(i)) {
			t.Fatalf("key %d Get failed after bounded reload", i)
		}
		p, _ := dst.GetProof(key(i))
		if !VerifyProof(wantRoot, p) {
			t.Fatalf("key %d proof failed after bounded reload", i)
		}
	}
}
