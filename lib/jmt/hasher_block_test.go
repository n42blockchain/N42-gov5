// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package jmt

import (
	"math/rand"
	"testing"

	"lukechampine.com/blake3"
)

// TestHashKeyMatchesBlake3 checks the one-block path against blake3.Sum256
// for every length up to two blocks, and HashTwo against Sum256 of the
// concatenation.
func TestHashKeyMatchesBlake3(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for n := 0; n <= 130; n++ {
		for r := 0; r < 8; r++ {
			key := make([]byte, n)
			rng.Read(key)
			if got, want := HashKey(key), Hash(blake3.Sum256(key)); got != want {
				t.Fatalf("len %d: HashKey %x, blake3 %x", n, got, want)
			}
		}
	}
	var a, b Hash
	rng.Read(a[:])
	rng.Read(b[:])
	if got, want := (Blake3Hasher{}).HashTwo(a, b), Hash(blake3.Sum256(append(a[:], b[:]...))); got != want {
		t.Fatalf("HashTwo %x, blake3 %x", got, want)
	}
}

func TestHashKeyNoAlloc(t *testing.T) {
	var addr [20]byte
	if n := testing.AllocsPerRun(100, func() { _ = HashKey(addr[:]) }); n != 0 {
		t.Fatalf("HashKey allocates %v", n)
	}
}
