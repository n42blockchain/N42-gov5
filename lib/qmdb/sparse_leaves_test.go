package qmdb

import "testing"

// TestSparseLeavesRoundTrip: encode/decode must reproduce the raw leaf layout
// exactly across sparse, dense, and empty twigs, and the sparse form must be
// much smaller for the typical mostly-dead sealed twig.
func TestSparseLeavesRoundTrip(t *testing.T) {
	shapes := map[string]func(n *[2 * TwigSize]Hash){
		"empty": func(n *[2 * TwigSize]Hash) {},
		"sparse10pct": func(n *[2 * TwigSize]Hash) {
			for i := 0; i < TwigSize; i += 10 {
				n[TwigSize+i] = key(uint64(i))
			}
		},
		"dense": func(n *[2 * TwigSize]Hash) {
			for i := 0; i < TwigSize; i++ {
				n[TwigSize+i] = key(uint64(i))
			}
		},
		"single": func(n *[2 * TwigSize]Hash) { n[TwigSize+2047] = key(7) },
	}
	for name, fill := range shapes {
		var nodes [2 * TwigSize]Hash
		fill(&nodes)
		enc := encodeSparseLeaves(&nodes)
		dec := decodeSparseLeaves(enc)
		if dec == nil {
			t.Fatalf("%s: decode failed", name)
		}
		for i := 0; i < TwigSize; i++ {
			var got Hash
			copy(got[:], dec[i*32:(i+1)*32])
			if got != nodes[TwigSize+i] {
				t.Fatalf("%s: leaf %d diverged", name, i)
			}
		}
		if name == "sparse10pct" && len(enc)*8 > TwigSize*32 {
			t.Fatalf("sparse blob not small: %d", len(enc))
		}
	}
	// Malformed inputs must return nil, not panic.
	if decodeSparseLeaves([]byte{sparseLeavesMarker, sparseLeavesVersion, 0xFF}) != nil {
		t.Fatal("short bitmap accepted")
	}
	var nodes [2 * TwigSize]Hash
	nodes[TwigSize] = key(1)
	enc := encodeSparseLeaves(&nodes)
	if decodeSparseLeaves(enc[:len(enc)-1]) != nil {
		t.Fatal("truncated leaf accepted")
	}
	if decodeSparseLeaves(append(enc, 0)) != nil {
		t.Fatal("trailing garbage accepted")
	}
}

// TestLegacyRawBlobStillReadable: a raw 64 KiB blob (pre-sparse format) must
// still hydrate through the LeafStore reader.
func TestLegacyRawBlobStillReadable(t *testing.T) {
	store := newMapStore()
	raw := make([]byte, TwigSize*32)
	k := key(3)
	copy(raw[5*32:6*32], k[:])
	_ = store.Put(LeavesTable, be8(0), raw)
	ls := LeafStoreFromGetter(store)
	got, ok := ls.Leaves(0)
	if !ok || len(got) != TwigSize*32 {
		t.Fatal("legacy raw blob not readable")
	}
	var h Hash
	copy(h[:], got[5*32:6*32])
	if h != k {
		t.Fatal("legacy raw blob content wrong")
	}
}
