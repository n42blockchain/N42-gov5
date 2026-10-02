// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sha3

import (
	"bytes"
	"crypto/rand"
	"testing"

	xsha3 "golang.org/x/crypto/sha3"
)

// TestSumAgainstReference checks every fixed-output variant against the
// golang.org/x/crypto/sha3 reference implementation across sizes that
// straddle the internal rate boundaries for each variant.
func TestSumAgainstReference(t *testing.T) {
	sizes := []int{0, 1, 16, 27, 72, 104, 135, 136, 137, 143, 144, 145, 167, 168, 169, 200, 1000, 10000}
	for _, n := range sizes {
		data := make([]byte, n)
		if _, err := rand.Read(data); err != nil {
			t.Fatal(err)
		}

		if got, want := Sum224(data), xsha3.Sum224(data); got != want {
			t.Errorf("Sum224(len=%d) mismatch: got %x want %x", n, got, want)
		}
		if got, want := Sum256(data), xsha3.Sum256(data); got != want {
			t.Errorf("Sum256(len=%d) mismatch: got %x want %x", n, got, want)
		}
		if got, want := Sum384(data), xsha3.Sum384(data); got != want {
			t.Errorf("Sum384(len=%d) mismatch: got %x want %x", n, got, want)
		}
		if got, want := Sum512(data), xsha3.Sum512(data); got != want {
			t.Errorf("Sum512(len=%d) mismatch: got %x want %x", n, got, want)
		}
	}
}

// TestShakeAgainstReference checks SHAKE128/256 output against the reference
// implementation for various output lengths, and across the rate boundary
// when writing input incrementally.
func TestShakeAgainstReference(t *testing.T) {
	outLens := []int{0, 1, 32, 63, 64, 65, 136, 168, 200, 1000}
	data := []byte("the quick brown fox jumps over the lazy dog, 1234567890")

	for _, n := range outLens {
		got := make([]byte, n)
		ShakeSum128(got, data)
		want := make([]byte, n)
		xsha3.ShakeSum128(want, data)
		if !bytes.Equal(got, want) {
			t.Errorf("ShakeSum128(outlen=%d) mismatch", n)
		}

		got256 := make([]byte, n)
		ShakeSum256(got256, data)
		want256 := make([]byte, n)
		xsha3.ShakeSum256(want256, data)
		if !bytes.Equal(got256, want256) {
			t.Errorf("ShakeSum256(outlen=%d) mismatch", n)
		}
	}
}

// TestIncrementalWrite verifies that writing data in small chunks that cross
// the rate boundary produces the same digest as a single write.
func TestIncrementalWrite(t *testing.T) {
	data := make([]byte, 5000)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}

	full := New256()
	_, _ = full.Write(data)
	var wantSum [32]byte
	full.Sum(wantSum[:0])

	chunked := New256()
	for i := 0; i < len(data); {
		step := 7
		if i+step > len(data) {
			step = len(data) - i
		}
		n, err := chunked.Write(data[i : i+step])
		if err != nil || n != step {
			t.Fatalf("Write error: n=%d err=%v", n, err)
		}
		i += step
	}
	var gotSum [32]byte
	chunked.Sum(gotSum[:0])

	if gotSum != wantSum {
		t.Errorf("chunked write mismatch: got %x want %x", gotSum, wantSum)
	}
}

// TestSumAppendsToPrefix ensures Sum appends to an existing slice without
// mutating the prefix, matching the hash.Hash contract.
func TestSumAppendsToPrefix(t *testing.T) {
	h := New256()
	_, _ = h.Write([]byte("hello"))
	prefix := []byte("PFX:")
	out := h.Sum(prefix)
	if !bytes.HasPrefix(out, prefix) {
		t.Fatalf("Sum did not preserve prefix: %x", out)
	}
	if len(out) != len(prefix)+32 {
		t.Fatalf("unexpected length: %d", len(out))
	}
}

// TestResetClearsState checks that Reset produces the same digest as a
// freshly constructed hash.
func TestResetClearsState(t *testing.T) {
	h := New512()
	_, _ = h.Write([]byte("garbage data to poison state"))
	h.Reset()
	_, _ = h.Write([]byte("hello"))
	var got [64]byte
	h.Sum(got[:0])

	want := Sum512([]byte("hello"))
	if got != want {
		t.Errorf("Reset mismatch: got %x want %x", got, want)
	}
}

// TestCloneIndependence checks that Clone produces an independent copy whose
// subsequent writes don't affect the original.
func TestCloneIndependence(t *testing.T) {
	sh := NewShake128()
	_, _ = sh.Write([]byte("shared prefix"))

	clone := sh.Clone()
	_, _ = sh.Write([]byte(" original tail"))
	_, _ = clone.Write([]byte(" clone tail"))

	outOrig := make([]byte, 32)
	_, _ = sh.Read(outOrig)
	outClone := make([]byte, 32)
	_, _ = clone.Read(outClone)

	if bytes.Equal(outOrig, outClone) {
		t.Fatalf("expected clone and original to diverge after different writes")
	}

	// Cross check against reference for the clone path.
	ref := xsha3.NewShake128()
	_, _ = ref.Write([]byte("shared prefix"))
	_, _ = ref.Write([]byte(" clone tail"))
	wantClone := make([]byte, 32)
	_, _ = ref.Read(wantClone)
	if !bytes.Equal(outClone, wantClone) {
		t.Errorf("clone digest mismatch: got %x want %x", outClone, wantClone)
	}
}

// TestShakeReadInChunks verifies Read can be called multiple times to
// retrieve output incrementally, matching one big Read.
func TestShakeReadInChunks(t *testing.T) {
	data := []byte("variable length output test")

	sh := NewShake256()
	_, _ = sh.Write(data)
	full := make([]byte, 300)
	_, _ = sh.Read(full)

	sh2 := NewShake256()
	_, _ = sh2.Write(data)
	chunked := make([]byte, 300)
	off := 0
	for off < len(chunked) {
		step := 17
		if off+step > len(chunked) {
			step = len(chunked) - off
		}
		n, err := sh2.Read(chunked[off : off+step])
		if err != nil {
			t.Fatalf("Read error: %v", err)
		}
		off += n
	}

	if !bytes.Equal(full, chunked) {
		t.Errorf("chunked read mismatch")
	}
}

// TestTurboShake exercises the TurboSHAKE variants and the domain separation
// byte validity checks.
func TestTurboShake(t *testing.T) {
	data := []byte("turbo shake input")

	out1 := make([]byte, 32)
	TurboShakeSum128(out1, data, 0x01)
	out2 := make([]byte, 32)
	TurboShakeSum128(out2, data, 0x02)
	if bytes.Equal(out1, out2) {
		t.Fatalf("different domain separation bytes produced identical output")
	}

	out3 := make([]byte, 64)
	TurboShakeSum256(out3, data, 0x1f)
	if len(out3) != 64 {
		t.Fatalf("unexpected length")
	}

	// Determinism: same D and data produce same output.
	out1b := make([]byte, 32)
	TurboShakeSum128(out1b, data, 0x01)
	if !bytes.Equal(out1, out1b) {
		t.Errorf("TurboShake128 not deterministic")
	}
}

// TestTurboShakeInvalidDomain checks that out-of-range domain separation
// bytes panic, as documented.
func TestTurboShakeInvalidDomain(t *testing.T) {
	cases := []byte{0x00, 0x80, 0xff}
	for _, d := range cases {
		func(d byte) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic for D=0x%02x", d)
				}
			}()
			NewTurboShake128(d)
		}(d)
	}
}

// TestSwitchDS checks that changing the domain separation byte changes the
// resulting digest for otherwise identical input.
func TestSwitchDS(t *testing.T) {
	h1 := New256()
	_, _ = h1.Write([]byte("data"))
	var out1 [32]byte
	h1.Sum(out1[:0])

	h2 := New256()
	h2.SwitchDS(0x1f)
	_, _ = h2.Write([]byte("data"))
	var out2 [32]byte
	h2.Sum(out2[:0])

	if out1 == out2 {
		t.Errorf("expected digests to differ after SwitchDS")
	}
}

// TestBlockSizeAndSize checks the reported block size and output size for
// each fixed-length variant.
func TestBlockSizeAndSize(t *testing.T) {
	cases := []struct {
		name          string
		h             State
		wantRate, len int
	}{
		{"224", New224(), 144, 28},
		{"256", New256(), 136, 32},
		{"384", New384(), 104, 48},
		{"512", New512(), 72, 64},
	}
	for _, c := range cases {
		if got := c.h.BlockSize(); got != c.wantRate {
			t.Errorf("%s: BlockSize() = %d, want %d", c.name, got, c.wantRate)
		}
		if got := c.h.Size(); got != c.len {
			t.Errorf("%s: Size() = %d, want %d", c.name, got, c.len)
		}
	}
}

// TestKeccakF1600AgainstManualXOR sanity-checks that two independent
// invocations of KeccakF1600 on the same state produce the same result
// (determinism) and that turbo vs non-turbo differ.
func TestKeccakF1600Deterministic(t *testing.T) {
	var a, b [25]uint64
	for i := range a {
		a[i] = uint64(i*7 + 1)
		b[i] = a[i]
	}
	KeccakF1600(&a, false)
	KeccakF1600(&b, false)
	if a != b {
		t.Fatalf("KeccakF1600 not deterministic")
	}

	var c [25]uint64
	for i := range c {
		c[i] = uint64(i*7 + 1)
	}
	KeccakF1600(&c, true)
	if c == a {
		t.Fatalf("turbo and non-turbo permutations should differ")
	}
}
