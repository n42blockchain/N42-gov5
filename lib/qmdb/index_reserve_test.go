package qmdb

import "testing"

// TestReserveOnlyReplacesEmptyMap pins what reserve is allowed to touch. A
// populated index holds the tree's live set, so replacing it would silently
// drop every mapping and later overwrites would deactivate the wrong slots —
// the same failure NewMapIndex's doc warns about for stale indexes.
func TestReserveOnlyReplacesEmptyMap(t *testing.T) {
	empty := newMapIndex()
	got := reserve(empty, 1000)
	if _, ok := got.(mapIndex); !ok {
		t.Fatal("reserve returned a non-map index for an empty map")
	}
	if got.Len() != 0 {
		t.Fatalf("reserved index is not empty: %d", got.Len())
	}

	populated := newMapIndex()
	populated.Put(Hash{0x01}, 7)
	if kept := reserve(populated, 1000); kept.Len() != 1 {
		t.Fatalf("reserve replaced a populated index (len=%d)", kept.Len())
	}
	if s, ok := reserve(populated, 1000).Get(Hash{0x01}); !ok || s != 7 {
		t.Fatalf("reserve lost a mapping: %d,%v", s, ok)
	}

	// A non-positive hint is not a reason to allocate anything.
	if reserve(empty, 0) == nil {
		t.Fatal("reserve returned nil for a zero hint")
	}
}

// TestReserveLeavesForeignIndexAlone: an MDBX-backed index is not ours to swap.
func TestReserveLeavesForeignIndexAlone(t *testing.T) {
	foreign := &countingIndex{m: persistMap{}}
	if got := reserve(foreign, 1000); got != Index(foreign) {
		t.Fatal("reserve replaced a non-map index")
	}
}

