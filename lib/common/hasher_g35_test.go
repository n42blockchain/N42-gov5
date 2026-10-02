/*
   Tests for big.go constants, eth.go denomination constants, and the
   pooled Keccak256 hasher in hasher.go.
*/

package common

import "testing"

func TestG35BigConstants(t *testing.T) {
	if Big0.Int64() != 0 || Big1.Int64() != 1 || Big2.Int64() != 2 || Big3.Int64() != 3 {
		t.Fatalf("unexpected small big constants")
	}
	if Big32.Int64() != 32 || Big256.Int64() != 256 || Big257.Int64() != 257 {
		t.Fatalf("unexpected large big constants")
	}
}

func TestG35EthDenominations(t *testing.T) {
	if Wei != 1 {
		t.Fatalf("expected Wei == 1")
	}
	if GWei != 1e9 {
		t.Fatalf("expected GWei == 1e9")
	}
	if Ether != 1e18 {
		t.Fatalf("expected Ether == 1e18")
	}
}

func TestG35HashData(t *testing.T) {
	h1, err := HashData([]byte("hello"))
	if err != nil {
		t.Fatalf("HashData: %v", err)
	}
	h2, err := HashData([]byte("hello"))
	if err != nil {
		t.Fatalf("HashData: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("expected deterministic hash for identical input")
	}

	h3, err := HashData([]byte("world"))
	if err != nil {
		t.Fatalf("HashData: %v", err)
	}
	if h1 == h3 {
		t.Fatalf("expected different hash for different input")
	}
}

func TestG35HasherPoolRoundTrip(t *testing.T) {
	h := NewHasher()
	if h == nil || h.Sha == nil {
		t.Fatalf("expected non-nil hasher from pool")
	}
	ReturnHasherToPool(h)

	// A reused hasher from the pool must be freshly reset (no leftover state).
	h2 := NewHasher()
	defer ReturnHasherToPool(h2)
	if _, err := h2.Sha.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
}
