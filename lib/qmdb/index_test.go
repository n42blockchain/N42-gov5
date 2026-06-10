package qmdb

import "testing"

// persistMap is a shared key->slot store that survives a "reload" (two trees can
// point at the same map, modeling an MDBX table that persists across restarts).
type persistMap map[Hash]uint64

// countingIndex wraps a persistMap and counts Put/Delete, so a test can assert the
// reload fast path did NOT rebuild the index (zero Puts during LoadFrom).
type countingIndex struct {
	m       persistMap
	puts    int
	deletes int
}

func (c *countingIndex) Get(k Hash) (uint64, bool) { s, ok := c.m[k]; return s, ok }
func (c *countingIndex) Put(k Hash, s uint64)      { c.puts++; c.m[k] = s }
func (c *countingIndex) Delete(k Hash)             { c.deletes++; delete(c.m, k) }
func (c *countingIndex) Len() int                  { return len(c.m) }

// TestInjectedIndexEquivalence: a tree driven through an injected Index behaves
// identically to the default in-RAM map index across churn (same roots, values).
func TestInjectedIndexEquivalence(t *testing.T) {
	def := New() // default map index
	inj := New()
	inj.SetIndex(&countingIndex{m: persistMap{}})

	rng := uint64(0x243F6A8885A308D3)
	next := func() uint64 { rng = rng*6364136223846793005 + 1442695040888963407; return rng >> 11 }
	const keys = 3000
	for round := 0; round < 50; round++ {
		for op := 0; op < 120; op++ {
			k := next() % keys
			if next()%5 == 0 {
				def.Delete(key(k))
				inj.Delete(key(k))
			} else {
				v := val(next())
				def.Set(key(k), v)
				inj.Set(key(k), v)
			}
		}
		if def.Root() != inj.Root() {
			t.Fatalf("round %d: injected-index root %x != default %x", round, inj.Root(), def.Root())
		}
	}
	if def.LiveCount() != inj.LiveCount() {
		t.Fatalf("live count diverged: default=%d injected=%d", def.LiveCount(), inj.LiveCount())
	}
}

// TestPersistentIndexResumeSkipsRebuild: when the injected index is already
// populated (persistent, as MDBX would be after a restart), LoadFrom must take the
// fast path — trusting the on-disk twig roots + index — and NOT rescan the entry
// log into the index. Asserted by zero Put calls during reload, with the root and
// live values still correct.
func TestPersistentIndexResumeSkipsRebuild(t *testing.T) {
	store := newMapStore()
	shared := persistMap{} // the "MDBX table" that persists across the reload

	src := New()
	src.SetIndex(&countingIndex{m: shared})
	src.SetCold(ColdReaderFromGetter(store))
	const n = 9000 // ~5 twigs
	for i := uint64(0); i < n; i++ {
		src.Set(key(i), val(i))
	}
	// churn so some slots die and the index has updates
	for i := uint64(0); i < n; i += 4 {
		src.Set(key(i), val(i+1))
	}
	for i := uint64(1); i < n; i += 7 {
		src.Delete(key(i))
	}
	if _, _, err := src.FlushTo(store, 0); err != nil {
		t.Fatalf("flush: %v", err)
	}
	wantRoot := src.Root()
	wantLive := src.LiveCount()

	// Reload into a fresh tree that shares the SAME (already-populated) index.
	ci := &countingIndex{m: shared}
	dst := New()
	dst.SetIndex(ci)
	if err := dst.LoadFrom(store); err != nil {
		t.Fatalf("load: %v", err)
	}
	if ci.puts != 0 {
		t.Fatalf("fast path violated: LoadFrom rebuilt the index with %d Puts", ci.puts)
	}
	if dst.Root() != wantRoot {
		t.Fatalf("reload root mismatch: want=%x got=%x", wantRoot, dst.Root())
	}
	if dst.LiveCount() != wantLive {
		t.Fatalf("reload live count mismatch: want=%d got=%d", wantLive, dst.LiveCount())
	}
	if dst.ResidentTwigLeaves() > 2 {
		t.Fatalf("fast-path reload kept too many twig leaves: %d", dst.ResidentTwigLeaves())
	}
	// live values + proofs resolve after the index-trusting reload
	for i := uint64(0); i < n; i += 13 {
		sv, sok := src.Get(key(i))
		dv, dok := dst.Get(key(i))
		if sok != dok || (sok && string(sv) != string(dv)) {
			t.Fatalf("key %d diverged after fast-path reload", i)
		}
		if dok {
			p, _ := dst.GetProof(key(i))
			if !VerifyProof(wantRoot, p) {
				t.Fatalf("key %d proof failed after fast-path reload", i)
			}
		}
	}
}
