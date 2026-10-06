package qmdb

import (
	"os"
	"testing"
)

// TestIndexRangeAndLookup exercises mapIndex.Range plus Tree.liveLen/IndexLookup/
// IndexRange, which only the MDBX-backed index path exercises in production.
func TestIndexRangeAndLookup(t *testing.T) {
	tr := New()
	tr.SetIndex(newMapIndex()) // default flatIndex is not an IterableIndex
	for i := uint64(0); i < 10; i++ {
		tr.Set(key(i), val(i))
	}
	if got := tr.liveLen(); got != 10 {
		t.Fatalf("liveLen = %d, want 10", got)
	}
	seen := map[Hash]uint64{}
	ok := tr.IndexRange(func(kh Hash, slot uint64) bool {
		seen[kh] = slot
		return true
	})
	if !ok {
		t.Fatalf("IndexRange returned false for an IterableIndex (mapIndex)")
	}
	if len(seen) != 10 {
		t.Fatalf("IndexRange visited %d entries, want 10", len(seen))
	}
	for i := uint64(0); i < 10; i++ {
		slot, found := tr.IndexLookup(key(i))
		if !found {
			t.Fatalf("IndexLookup miss for key %d", i)
		}
		if got, ok := seen[key(i)]; !ok || got != slot {
			t.Fatalf("IndexRange slot mismatch for key %d: range=%d lookup=%d", i, got, slot)
		}
	}

	// Early termination: fn returning false stops the walk.
	count := 0
	tr.IndexRange(func(Hash, uint64) bool {
		count++
		return false
	})
	if count != 1 {
		t.Fatalf("IndexRange did not stop early, visited %d", count)
	}

	// IndexStats reflects puts made above (hits/misses come from Get activity
	// elsewhere in the suite, so only assert puts is non-zero and monotonic).
	_, _, puts, _ := IndexStats()
	if puts == 0 {
		t.Fatalf("IndexStats reported zero puts after 10 Sets")
	}
}

// TestIndexRangeNonIterable checks the false path: a non-IterableIndex index
// (truncIndex does not implement Range) makes IndexRange report false.
func TestIndexRangeNonIterable(t *testing.T) {
	tr := New()
	tr.Set(key(1), val(1))
	tr.SetIndex(NewTruncIndex(tr.SlotKeyResolver(), 0))
	// Re-seed the trunc index the same way NewIndexFor would populate a fresh
	// index: re-apply the already-live key so the index has an entry.
	tr.idx.Put(key(1), 0)
	if _, ok := tr.idx.(IterableIndex); ok {
		t.Skip("truncIndex unexpectedly implements IterableIndex")
	}
	if ok := tr.IndexRange(func(Hash, uint64) bool { return true }); ok {
		t.Fatalf("IndexRange should report false for a non-iterable index")
	}
}

// TestAdoptFlushed covers the "another tree flushed our identical slots" path:
// dead-row bookkeeping drops and eviction proceeds exactly like our own flush.
func TestAdoptFlushed(t *testing.T) {
	tr := New()
	const n = uint64(3 * TwigSize)
	for i := uint64(0); i < n; i++ {
		tr.Set(key(i), val(i))
	}
	// Create some dead rows via overwrite so deadFlushed/stagedDead are non-empty.
	for i := uint64(0); i < n; i += 2 {
		tr.Set(key(i), val(i+1000))
	}
	store := newMapStore()
	if _, _, err := tr.FlushTo(store, 0); err != nil {
		t.Fatalf("FlushTo: %v", err)
	}
	before := tr.ResidentEntries()
	tr.AdoptFlushed(n)
	after := tr.ResidentEntries()
	if after > before {
		t.Fatalf("AdoptFlushed grew resident entries: before=%d after=%d", before, after)
	}
	if len(tr.deadFlushed) != 0 || len(tr.stagedDead) != 0 {
		t.Fatalf("AdoptFlushed left dead-row bookkeeping: deadFlushed=%d stagedDead=%d",
			len(tr.deadFlushed), len(tr.stagedDead))
	}
}

// TestLeafAndForceDirty covers twig.leaf (direct hash read) and Tree.ForceDirty
// (marks every resident, non-pruned twig dirty so the next Root() recomputes).
func TestLeafAndForceDirty(t *testing.T) {
	tr := New()
	const n = uint64(2*TwigSize + 5)
	for i := uint64(0); i < n; i++ {
		tr.Set(key(i), val(i))
	}
	root1 := tr.Root()

	tw := tr.twigs[0]
	if tw == nil {
		t.Fatalf("twig 0 missing")
	}
	h := tw.leaf(3)
	if h == (Hash{}) {
		t.Fatalf("twig.leaf(3) returned zero hash for a live slot")
	}

	tr.ForceDirty()
	for i, tw := range tr.twigs {
		if tw != nil && !tw.pruned && tw.nodes != nil && !tw.dirty {
			t.Fatalf("ForceDirty left twig %d clean", i)
		}
	}
	if !tr.rootDirty {
		t.Fatalf("ForceDirty did not mark rootDirty")
	}
	root2 := tr.Root()
	if root1 != root2 {
		t.Fatalf("ForceDirty changed the committed root: %x != %x", root1, root2)
	}
}

// TestGetVia covers the alternate-reader Get path used by a reader holding its
// own cold transaction instead of the tree owner's.
func TestGetVia(t *testing.T) {
	tr := New()
	const n = uint64(3 * TwigSize)
	for i := uint64(0); i < n; i++ {
		tr.Set(key(i), val(i))
	}
	store := newMapStore()
	if _, _, err := tr.FlushTo(store, 0); err != nil {
		t.Fatalf("FlushTo: %v", err)
	}
	tr.EvictThrough(n)

	cold := ColdReaderFromGetter(store)

	// Resident-window read via GetVia (slot >= entriesBase).
	v, found, evicted := tr.GetVia(key(n-1), cold)
	if !found || evicted {
		t.Fatalf("GetVia resident lookup: found=%v evicted=%v", found, evicted)
	}
	if string(v) != string(val(n-1)) {
		t.Fatalf("GetVia resident value mismatch")
	}

	// Evicted-window read, hydrated through the supplied cold reader.
	v, found, evicted = tr.GetVia(key(0), cold)
	if !found || evicted {
		t.Fatalf("GetVia evicted-but-cold-hit lookup: found=%v evicted=%v", found, evicted)
	}
	if string(v) != string(val(0)) {
		t.Fatalf("GetVia evicted value mismatch")
	}

	// Miss: a key never inserted.
	_, found, evicted = tr.GetVia(key(n+1000), cold)
	if found {
		t.Fatalf("GetVia reported found for a key never set")
	}
	_ = evicted
}

// TestSlotActive covers the committed-liveness-bit accessor directly, including
// the out-of-range and unhydrated-twig false paths.
func TestSlotActive(t *testing.T) {
	tr := New()
	const n = uint64(2*TwigSize + 10)
	for i := uint64(0); i < n; i++ {
		tr.Set(key(i), val(i))
	}
	tr.Delete(key(1))

	if active, ok := tr.SlotActive(0); !ok || !active {
		t.Fatalf("SlotActive(0) = %v,%v want true,true", active, ok)
	}
	if active, ok := tr.SlotActive(n + 1); ok || active {
		t.Fatalf("SlotActive(out of range) = %v,%v want false,false", active, ok)
	}

	// Force an unhydrated/absent twig by asking beyond nextSlot bounds inside a
	// twig index that was never allocated.
	if active, ok := tr.SlotActive(1 << 40); ok || active {
		t.Fatalf("SlotActive(huge slot) = %v,%v want false,false", active, ok)
	}
}

// TestAbortFlushAndOnDiskBytes covers the rollback-requeue path and the
// footprint accessor used for apples-to-apples sizing against other trees.
func TestAbortFlushAndOnDiskBytes(t *testing.T) {
	tr := New()
	store := newMapStore()
	tr.SetCold(ColdReaderFromGetter(store))
	tr.SetLeafStore(LeafStoreFromGetter(store))
	const n = uint64(3 * TwigSize)
	for i := uint64(0); i < n; i++ {
		tr.Set(key(i), val(i))
	}
	next, _, err := tr.FlushTo(store, 0)
	if err != nil {
		t.Fatalf("FlushTo #1: %v", err)
	}
	tr.CommitFlush()
	tr.EvictThrough(n) // advance entriesBase so overwrites below it register as dead

	for i := uint64(0); i < n; i += 5 {
		tr.Set(key(i), val(i+1)) // overwrite a flushed-and-evicted slot: deadFlushed
	}
	if len(tr.deadFlushed) == 0 {
		t.Fatalf("expected non-empty deadFlushed after overwriting evicted slots")
	}
	if _, _, err := tr.FlushTo(store, next); err != nil {
		t.Fatalf("FlushTo #2: %v", err)
	}
	stagedBefore := len(tr.stagedDead)
	if stagedBefore == 0 {
		t.Fatalf("expected non-empty stagedDead after a flush with overwrites")
	}
	deadBefore := len(tr.deadFlushed)

	tr.AbortFlush()
	if len(tr.stagedDead) != 0 {
		t.Fatalf("AbortFlush left stagedDead non-empty: %d", len(tr.stagedDead))
	}
	if len(tr.deadFlushed) != deadBefore+stagedBefore {
		t.Fatalf("AbortFlush did not re-queue: deadFlushed=%d want=%d",
			len(tr.deadFlushed), deadBefore+stagedBefore)
	}

	entryBytes, twigBytes := tr.OnDiskBytes()
	if entryBytes <= 0 || twigBytes <= 0 {
		t.Fatalf("OnDiskBytes = (%d,%d), want both > 0", entryBytes, twigBytes)
	}
}

// TestTruncIndexEnabledAndNewIndexFor covers the env-gated index shape switch
// and the two in-RAM index constructors it chooses between.
func TestTruncIndexEnabledAndNewIndexFor(t *testing.T) {
	const envVar = "N42_QMDB_TRUNC_INDEX"
	old, hadOld := os.LookupEnv(envVar)
	t.Cleanup(func() {
		if hadOld {
			os.Setenv(envVar, old)
		} else {
			os.Unsetenv(envVar)
		}
	})

	os.Unsetenv(envVar)
	if truncIndexEnabled() {
		t.Fatalf("truncIndexEnabled() = true with env unset, want false")
	}
	tr := New()
	idx := NewIndexFor(tr, 16)
	if _, ok := idx.(mapIndex); !ok {
		t.Fatalf("NewIndexFor with trunc disabled returned %T, want mapIndex", idx)
	}
	idx = NewIndexFor(tr, 0)
	if _, ok := idx.(mapIndex); !ok {
		t.Fatalf("NewIndexFor sizeHint=0 returned %T, want mapIndex", idx)
	}

	// IndexOverflow / IndexOverflowReasons: -1,-1 for a non-trunc index, real
	// counters for a trunc index. Check the non-trunc case while still disabled.
	mi := NewIndexFor(tr, 0)
	if got := IndexOverflow(mi); got != -1 {
		t.Fatalf("IndexOverflow(mapIndex) = %d, want -1", got)
	}
	if c, u := IndexOverflowReasons(mi); c != -1 || u != -1 {
		t.Fatalf("IndexOverflowReasons(mapIndex) = (%d,%d), want (-1,-1)", c, u)
	}

	os.Setenv(envVar, "true")
	if !truncIndexEnabled() {
		t.Fatalf("truncIndexEnabled() = false with env=true, want true")
	}
	idx = NewIndexFor(tr, 16)
	if _, ok := idx.(*truncIndex); !ok {
		t.Fatalf("NewIndexFor with trunc enabled returned %T, want *truncIndex", idx)
	}

	ti := NewIndexFor(tr, 16)
	if got := IndexOverflow(ti); got < 0 {
		t.Fatalf("IndexOverflow(truncIndex) = %d, want >= 0", got)
	}
	if c, u := IndexOverflowReasons(ti); c < 0 || u < 0 {
		t.Fatalf("IndexOverflowReasons(truncIndex) = (%d,%d), want both >= 0", c, u)
	}
}
