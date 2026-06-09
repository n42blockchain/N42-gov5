package qmdb

import "testing"

// mapStore implements both Putter and Getter over an in-memory table->key->value
// map, so a FlushTo can be reloaded by LoadFrom in the same test.
type mapStore map[string]map[string][]byte

func newMapStore() mapStore { return mapStore{} }

func (m mapStore) Put(table string, key, value []byte) error {
	t := m[table]
	if t == nil {
		t = map[string][]byte{}
		m[table] = t
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	t[string(key)] = cp
	return nil
}

func (m mapStore) GetOne(table string, key []byte) ([]byte, error) {
	if t := m[table]; t != nil {
		return t[string(key)], nil
	}
	return nil, nil
}

// TestFlushReloadRoundTrip: flushing a tree's positional layout and reloading it
// into a fresh tree must reproduce the exact world root, live set, and proofs.
// This is the cross-process resume path for the (history-dependent) QMDB root.
func TestFlushReloadRoundTrip(t *testing.T) {
	src := New()
	const n = 7000 // > 3 twigs
	for i := uint64(0); i < n; i++ {
		src.Set(key(i), val(i))
	}
	// churn: overwrite some (dead slots) and delete some (active-bit must clear).
	for i := uint64(0); i < n; i += 3 {
		src.Set(key(i), val(i+100))
	}
	for i := uint64(1); i < n; i += 9 {
		src.Delete(key(i))
	}
	want := src.Root()
	wantLive := src.LiveCount()

	store := newMapStore()
	if _, _, err := src.FlushTo(store, 0); err != nil {
		t.Fatalf("flush: %v", err)
	}

	dst := New()
	if err := dst.LoadFrom(store); err != nil {
		t.Fatalf("load: %v", err)
	}
	if dst.Root() != want {
		t.Fatalf("reload root mismatch:\n  want=%x\n  got =%x", want, dst.Root())
	}
	if dst.LiveCount() != wantLive {
		t.Fatalf("reload live count mismatch: want=%d got=%d", wantLive, dst.LiveCount())
	}
	// nextSlot (append cursor) must survive so further appends do not collide.
	if dst.NextSlot() != src.NextSlot() {
		t.Fatalf("reload nextSlot mismatch: want=%d got=%d", src.NextSlot(), dst.NextSlot())
	}
	// every live key resolves to the same value after reload
	for i := uint64(0); i < n; i++ {
		sv, sok := src.Get(key(i))
		dv, dok := dst.Get(key(i))
		if sok != dok {
			t.Fatalf("key %d liveness diverged: src=%v dst=%v", i, sok, dok)
		}
		if sok && string(sv) != string(dv) {
			t.Fatalf("key %d value diverged after reload", i)
		}
	}
	// continued updates on the reloaded tree stay consistent with the same
	// updates on the original (resume correctness).
	src.Set(key(99999), val(7))
	dst.Set(key(99999), val(7))
	if src.Root() != dst.Root() {
		t.Fatalf("post-reload append diverged: src=%x dst=%x", src.Root(), dst.Root())
	}
}

// TestReloadEmptyStore: loading from a never-flushed store leaves an empty tree.
func TestReloadEmptyStore(t *testing.T) {
	dst := New()
	if err := dst.LoadFrom(newMapStore()); err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if dst.Root() != nullHash || dst.LiveCount() != 0 {
		t.Fatal("empty-store reload should yield empty tree")
	}
}
