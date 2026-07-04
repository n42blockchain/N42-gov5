package main

import "testing"

// TestRecordChangeStorageScratchClobber: two slots sharing domain and first
// nibble but differing at nibble 1 must produce DISTINCT stoDirty[2] keys.
func TestRecordChangeStorageScratchClobber(t *testing.T) {
	b := newMarkBuilder(t)
	dom := make([]byte, 40)
	for i := range dom[:32] {
		dom[i] = 0xAA
	}
	nibsA := make([]byte, 64)
	nibsB := make([]byte, 64)
	nibsA[0], nibsA[1], nibsA[2] = 0xb, 0xc, 0x1
	nibsB[0], nibsB[1], nibsB[2] = 0xb, 0xf, 0x2
	b.recordChangeStorage(dom, nibsA, 100)
	b.recordChangeStorage(dom, nibsB, 100)
	if got := len(b.stoDirty[2]); got != 2 {
		for k := range b.stoDirty[2] {
			t.Logf("stoDirty[2] key %x", []byte(k))
		}
		t.Fatalf("stoDirty[2] has %d keys, want 2 (scratch clobber folded them)", got)
	}
}
