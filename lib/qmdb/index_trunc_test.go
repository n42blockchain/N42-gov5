package qmdb

import "testing"

// fakeSlots is a stand-in for the entry log: slot -> key hash.
type fakeSlots map[uint64]Hash

func (f fakeSlots) resolve(slot uint64) (Hash, bool) {
	k, ok := f[slot]
	return k, ok
}

func keyWithPrefix(prefixByte, tailByte byte) Hash {
	var h Hash
	for i := 0; i < 16; i++ {
		h[i] = prefixByte
	}
	h[31] = tailByte // same 16-byte prefix, different key
	return h
}

func TestTruncIndexRoundTrip(t *testing.T) {
	slots := fakeSlots{}
	idx := NewTruncIndex(slots.resolve, 16)

	for i := 0; i < 200; i++ {
		var h Hash
		h[0], h[1], h[31] = byte(i), byte(i>>8), byte(i*7)
		slots[uint64(i)] = h
		idx.Put(h, uint64(i))
	}
	if idx.Len() != 200 {
		t.Fatalf("Len = %d, want 200", idx.Len())
	}
	for i := 0; i < 200; i++ {
		var h Hash
		h[0], h[1], h[31] = byte(i), byte(i>>8), byte(i*7)
		s, ok := idx.Get(h)
		if !ok || s != uint64(i) {
			t.Fatalf("Get(key %d) = %d,%v", i, s, ok)
		}
	}
	var absent Hash
	absent[0] = 0xEE
	if _, ok := idx.Get(absent); ok {
		t.Fatal("Get returned a slot for a key never put")
	}
}

// TestTruncIndexCollisionKeepsBothMappings is the property the whole design
// rests on. Two live keys sharing a prefix must both resolve to their own slot:
// the map holds one slot per prefix, so without the overflow map the second Put
// would silently drop the first key's mapping and the tree would later
// deactivate the wrong entry.
func TestTruncIndexCollisionKeepsBothMappings(t *testing.T) {
	a := keyWithPrefix(0xAB, 0x01)
	b := keyWithPrefix(0xAB, 0x02)
	if prefixOf(a) != prefixOf(b) {
		t.Fatal("fixture keys do not actually collide")
	}
	slots := fakeSlots{10: a, 20: b}
	idx := NewTruncIndex(slots.resolve, 4)

	idx.Put(a, 10)
	idx.Put(b, 20)

	if s, ok := idx.Get(a); !ok || s != 10 {
		t.Fatalf("first key lost its mapping: %d,%v", s, ok)
	}
	if s, ok := idx.Get(b); !ok || s != 20 {
		t.Fatalf("colliding key lost its mapping: %d,%v", s, ok)
	}
	if idx.Len() != 2 {
		t.Fatalf("Len = %d, want 2", idx.Len())
	}
	if n := idx.(*truncIndex).OverflowLen(); n != 1 {
		t.Fatalf("overflow holds %d, want 1", n)
	}
}

// TestTruncIndexCollisionDelete: deleting one of two colliding keys must not
// take the other's mapping with it.
func TestTruncIndexCollisionDelete(t *testing.T) {
	a := keyWithPrefix(0xCD, 0x01)
	b := keyWithPrefix(0xCD, 0x02)
	slots := fakeSlots{10: a, 20: b}
	idx := NewTruncIndex(slots.resolve, 4)
	idx.Put(a, 10)
	idx.Put(b, 20)

	idx.Delete(b) // the overflow one
	if s, ok := idx.Get(a); !ok || s != 10 {
		t.Fatalf("deleting the colliding key took the holder's mapping: %d,%v", s, ok)
	}
	if _, ok := idx.Get(b); ok {
		t.Fatal("deleted key still resolves")
	}

	idx.Put(b, 20)
	idx.Delete(a) // the prefix holder
	if _, ok := idx.Get(a); ok {
		t.Fatal("deleted holder still resolves")
	}
	if s, ok := idx.Get(b); !ok || s != 20 {
		t.Fatalf("deleting the holder dropped the colliding key: %d,%v", s, ok)
	}
}

// TestTruncIndexOverwriteSameKey: the ordinary case is a key moving to a new
// slot, which must replace rather than land in the overflow map.
func TestTruncIndexOverwriteSameKey(t *testing.T) {
	a := keyWithPrefix(0x11, 0x01)
	slots := fakeSlots{10: a}
	idx := NewTruncIndex(slots.resolve, 4)
	idx.Put(a, 10)
	slots[11] = a
	idx.Put(a, 11)

	if s, ok := idx.Get(a); !ok || s != 11 {
		t.Fatalf("Get = %d,%v, want 11", s, ok)
	}
	if idx.Len() != 1 {
		t.Fatalf("Len = %d, want 1", idx.Len())
	}
	if n := idx.(*truncIndex).OverflowLen(); n != 0 {
		t.Fatalf("re-slotting the same key went to overflow (%d)", n)
	}
}

// TestTruncIndexUnreadableSlotIsNotAMatch: a slot that cannot be read must not
// be assumed to hold the key being put. Guessing "probably ours" would
// overwrite another key's mapping.
func TestTruncIndexUnreadableSlotIsNotAMatch(t *testing.T) {
	a := keyWithPrefix(0x22, 0x01)
	b := keyWithPrefix(0x22, 0x02)
	slots := fakeSlots{} // resolves nothing
	idx := NewTruncIndex(slots.resolve, 4)

	idx.Put(a, 10)
	idx.Put(b, 20)
	if n := idx.(*truncIndex).OverflowLen(); n != 1 {
		t.Fatalf("unreadable holder was treated as a match; overflow=%d", n)
	}
	if s, ok := idx.Get(b); !ok || s != 20 {
		t.Fatalf("Get(b) = %d,%v, want 20", s, ok)
	}
}

// TestTruncIndexRequiresResolver: without one, a prefix hit cannot be told from
// a collision and the index would hand out another key's slot.
func TestTruncIndexRequiresResolver(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic when constructed without a resolver")
		}
	}()
	NewTruncIndex(nil, 4)
}
