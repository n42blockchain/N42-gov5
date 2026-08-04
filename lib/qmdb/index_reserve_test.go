package qmdb

import "testing"

// TestReserveOnlyReplacesEmptyMap pins what reserve is allowed to touch. A
// populated index holds the tree's live set, so replacing it would silently
// drop every mapping and later overwrites would deactivate the wrong slots —
// the same failure NewMapIndex's doc warns about for stale indexes.
func TestReserveOnlyReplacesEmptyMap(t *testing.T) {
	empty := newMapIndex()
	got, replaced := reserve(empty, 1000)
	if !replaced {
		t.Fatal("reserve did not report replacing an empty map")
	}
	if _, ok := got.(mapIndex); !ok {
		t.Fatal("reserve returned a non-map index for an empty map")
	}
	if got.Len() != 0 {
		t.Fatalf("reserved index is not empty: %d", got.Len())
	}

	populated := newMapIndex()
	populated.Put(Hash{0x01}, 7)
	kept, replaced := reserve(populated, 1000)
	if replaced {
		t.Fatal("reserve reported replacing a populated index")
	}
	if kept.Len() != 1 {
		t.Fatalf("reserve replaced a populated index (len=%d)", kept.Len())
	}
	if s, ok := kept.Get(Hash{0x01}); !ok || s != 7 {
		t.Fatalf("reserve lost a mapping: %d,%v", s, ok)
	}

	// A non-positive hint is not a reason to allocate anything.
	got, replaced = reserve(empty, 0)
	if replaced || got == nil {
		t.Fatal("reserve returned nil for a zero hint")
	}

	// The opt-out keeps the original map and must not claim a replacement.
	t.Setenv("N42_QMDB_INDEX_PRESIZE", "0")
	got, replaced = reserve(empty, 1000)
	if replaced || got == nil {
		t.Fatal("reserve replaced an index while pre-sizing was disabled")
	}
}

// TestReserveLeavesForeignIndexAlone: an MDBX-backed index is not ours to swap.
func TestReserveLeavesForeignIndexAlone(t *testing.T) {
	foreign := &countingIndex{m: persistMap{}}
	got, replaced := reserve(foreign, 1000)
	if replaced || got != Index(foreign) {
		t.Fatal("reserve replaced a non-map index")
	}
}

// TestReserveDoesNotCompareMaps pins the reason reserve reports replacement
// instead of letting the caller compare. Index's dynamic type here is a map,
// and comparing two interface values holding maps panics at run time — it is
// not a compile error, so nothing catches it until a load runs.
func TestReserveDoesNotCompareMaps(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("reserve's contract still forces a map comparison: %v", r)
		}
	}()
	a := newMapIndex()
	b, replaced := reserve(a, 128)
	if !replaced {
		t.Fatal("expected replacement")
	}
	_ = b.Len()
}
