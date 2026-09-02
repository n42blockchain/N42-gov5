// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/holiman/uint256"
)

// appendU256Reference is the implementation appendU256 replaced: uint256's own
// Bytes(), which allocates because the [32]byte it slices escapes.
func appendU256Reference(b []byte, v *uint256.Int) []byte {
	if v == nil {
		return append(b, 0xFF)
	}
	bs := v.Bytes()
	b = append(b, byte(len(bs)))
	return append(b, bs...)
}

// TestAppendU256MatchesReference pins the compact storage codec byte for byte.
// This encoding is what a block's transactions are PERSISTED as, so a
// difference here is not a performance regression, it is a database that reads
// back as different transactions.
func TestAppendU256MatchesReference(t *testing.T) {
	vals := []*uint256.Int{
		nil,
		uint256.NewInt(0),
		uint256.NewInt(1),
		uint256.NewInt(0xFF),
		uint256.NewInt(0x100),
		uint256.NewInt(0xFFFFFFFFFFFFFFFF),
		uint256.MustFromHex("0x10000000000000000"),
		uint256.MustFromHex("0xff00000000000000000000000000000000000000000000000000000000000000"),
		uint256.NewInt(0xff), // low byte only; leading-zero hex is rejected by uint256 v1.2.3
		new(uint256.Int).SetAllOne(),
	}
	// Every byte length 0..32 exactly, since the length byte is what changes.
	for n := 1; n <= 32; n++ {
		v := new(uint256.Int).SetOne()
		for i := 1; i < n; i++ {
			v.Lsh(v, 8)
		}
		vals = append(vals, v)
	}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		v := new(uint256.Int).SetUint64(rng.Uint64())
		v.Lsh(v, uint(rng.Intn(200)))
		v.AddUint64(v, rng.Uint64())
		vals = append(vals, v)
	}

	for i, v := range vals {
		// Non-empty prefix, so a bug that ignores the destination shows up.
		got := appendU256([]byte{0xAA, 0xBB}, v)
		want := appendU256Reference([]byte{0xAA, 0xBB}, v)
		if !bytes.Equal(got, want) {
			t.Fatalf("value %d (%v): got %x, want %x", i, v, got, want)
		}
	}
}

// TestAppendU256DoesNotAllocate is the point of the change: five or six of
// these run per transaction, and a block carries 22,857 of them.
func TestAppendU256DoesNotAllocate(t *testing.T) {
	v := uint256.MustFromHex("0x8b5e7f3a1c9d4e2b6a0f8c3d5e7a9b1c2d4e6f8a0b1c3d5e7f9a1b3c5d7e9f01")
	dst := make([]byte, 0, 64)
	if n := testing.AllocsPerRun(200, func() {
		_ = appendU256(dst[:0], v)
	}); n != 0 {
		t.Fatalf("appendU256 allocated %.1f times per call, want 0", n)
	}
}
