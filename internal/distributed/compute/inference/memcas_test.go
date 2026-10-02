package inference

import (
	"bytes"
	"testing"
)

// TestMemCAS_StoreAndLoad covers the basic round trip: data stored under its
// content hash must load back byte-identical, and the loaded slice must be
// an independent copy (mutating it should not corrupt the store).
func TestMemCAS_StoreAndLoad(t *testing.T) {
	c := NewMemCAS()
	data := []byte("hello inference cas")

	hash, err := c.Store(data)
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	got, err := c.Load(hash)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Load() = %q, want %q", got, data)
	}

	// Mutate the returned slice; the stored copy must be unaffected.
	got[0] = 'X'
	got2, err := c.Load(hash)
	if err != nil {
		t.Fatalf("Load() second call error = %v", err)
	}
	if !bytes.Equal(got2, data) {
		t.Fatalf("Store did not defensively copy: second Load() = %q, want %q", got2, data)
	}
}

// TestMemCAS_LoadMissing covers the not-found error branch.
func TestMemCAS_LoadMissing(t *testing.T) {
	c := NewMemCAS()
	var zero [32]byte
	_, err := c.Load(zero)
	if err == nil {
		t.Fatal("Load() error = nil, want error for missing hash")
	}
}

// TestMemCAS_StoreIsContentAddressed covers that identical content always
// maps to the same hash and distinct content maps to distinct hashes.
func TestMemCAS_StoreIsContentAddressed(t *testing.T) {
	c := NewMemCAS()
	h1, _ := c.Store([]byte("same"))
	h2, _ := c.Store([]byte("same"))
	if h1 != h2 {
		t.Fatalf("Store() of identical content produced different hashes: %x != %x", h1, h2)
	}

	h3, _ := c.Store([]byte("different"))
	if h1 == h3 {
		t.Fatal("Store() of different content produced the same hash")
	}
}
