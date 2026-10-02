// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for the crypto/rand-backed helpers in secure_rand.go: range bounds,
// panics on invalid input, and basic sanity of the byte/permutation outputs.

package misc

import "testing"

func TestG35SecureIntn(t *testing.T) {
	for i := 0; i < 50; i++ {
		v := SecureIntn(10)
		if v < 0 || v >= 10 {
			t.Fatalf("SecureIntn(10) out of range: %d", v)
		}
	}
}

func TestG35SecureIntnPanicsOnNonPositive(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic for n<=0")
		}
	}()
	SecureIntn(0)
}

func TestG35SecureInt63n(t *testing.T) {
	for i := 0; i < 50; i++ {
		v := SecureInt63n(100)
		if v < 0 || v >= 100 {
			t.Fatalf("SecureInt63n(100) out of range: %d", v)
		}
	}
}

func TestG35SecureInt63nPanicsOnNonPositive(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic for n<=0")
		}
	}()
	SecureInt63n(-1)
}

func TestG35SecureUint64(t *testing.T) {
	// Collect a handful of values; just exercise the code path deterministically
	// (collisions across 2^64 space are not something a short test should assert).
	for i := 0; i < 5; i++ {
		_ = SecureUint64()
	}
}

func TestG35SecureBytes(t *testing.T) {
	b := make([]byte, 16)
	SecureBytes(b)
	allZero := true
	for _, v := range b {
		if v != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Fatalf("expected SecureBytes to fill non-zero random bytes (astronomically unlikely all-zero)")
	}
}

func TestG35SecureFloat64(t *testing.T) {
	for i := 0; i < 50; i++ {
		v := SecureFloat64()
		if v < 0.0 || v >= 1.0 {
			t.Fatalf("SecureFloat64 out of [0,1): %v", v)
		}
	}
}

func TestG35SecurePerm(t *testing.T) {
	p := SecurePerm(10)
	if len(p) != 10 {
		t.Fatalf("expected permutation of length 10, got %d", len(p))
	}
	seen := make(map[int]bool, 10)
	for _, v := range p {
		if v < 0 || v >= 10 {
			t.Fatalf("permutation value out of range: %d", v)
		}
		if seen[v] {
			t.Fatalf("duplicate value %d in permutation", v)
		}
		seen[v] = true
	}
}

func TestG35SecurePermPanicsOnNonPositive(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic for n<=0")
		}
	}()
	SecurePerm(0)
}
