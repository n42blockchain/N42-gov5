// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

// Known-answer-test vectors for the baseline precompiles (0x01-0x09),
// exercised directly via their Run() method rather than the fuzz harness
// in contracts_fuzz_test.go.

package vm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/n42blockchain/N42/crypto"
	"golang.org/x/crypto/ripemd160" //nolint:staticcheck // reference implementation for the KAT, not production use
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("invalid hex %q: %v", s, err)
	}
	return b
}

func TestPrecompileEcrecoverKAT(t *testing.T) {
	// Sign a known hash with a freshly generated key and verify the
	// ecrecover precompile recovers the matching address, rather than
	// relying on a hand-copied third-party vector.
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	wantAddr := crypto.PubkeyToAddress(key.PublicKey)

	var hash [32]byte
	copy(hash[:], []byte("ecrecover known-answer test msg"))

	sig, err := crypto.Sign(hash[:], key)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	r := sig[:32]
	s := sig[32:64]
	v := sig[64] + 27 // ecrecover precompile expects v in {27,28}

	input := make([]byte, 128)
	copy(input[0:32], hash[:])
	input[63] = v
	copy(input[64:96], r)
	copy(input[96:128], s)

	c := &ecrecover{}
	got, err := c.Run(input)
	if err != nil {
		t.Fatalf("ecrecover.Run error: %v", err)
	}
	want := make([]byte, 32)
	copy(want[12:], wantAddr.Bytes())
	if !bytes.Equal(got, want) {
		t.Errorf("ecrecover = %x, want %x", got, want)
	}
}

func TestPrecompileEcrecoverInvalidSigReturnsEmpty(t *testing.T) {
	// v out of {27,28} range must yield empty output, not an error.
	input := make([]byte, 128)
	input[63] = 0x01 // v = 1, invalid
	c := &ecrecover{}
	got, err := c.Run(input)
	if err != nil {
		t.Fatalf("ecrecover.Run error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ecrecover with invalid v = %x, want empty", got)
	}
}

func TestPrecompileSha256KAT(t *testing.T) {
	c := &sha256hash{}
	got, err := c.Run([]byte("abc"))
	if err != nil {
		t.Fatalf("sha256hash.Run error: %v", err)
	}
	digest := sha256.Sum256([]byte("abc"))
	want := digest[:]
	if !bytes.Equal(got, want) {
		t.Errorf("sha256(\"abc\") = %x, want %x", got, want)
	}
}

func TestPrecompileRipemd160KAT(t *testing.T) {
	c := &ripemd160hash{}
	got, err := c.Run([]byte("abc"))
	if err != nil {
		t.Fatalf("ripemd160hash.Run error: %v", err)
	}
	// RIPEMD-160 digest, left-padded to 32 bytes per EVM convention.
	h := ripemd160.New()
	h.Write([]byte("abc"))
	digest := h.Sum(nil)
	want := make([]byte, 32)
	copy(want[32-len(digest):], digest)
	if !bytes.Equal(got, want) {
		t.Errorf("ripemd160(\"abc\") = %x, want %x", got, want)
	}
}

func TestPrecompileIdentityKAT(t *testing.T) {
	c := &dataCopy{}
	input := []byte("the quick brown fox")
	got, err := c.Run(input)
	if err != nil {
		t.Fatalf("dataCopy.Run error: %v", err)
	}
	if !bytes.Equal(got, input) {
		t.Errorf("identity(%q) = %x, want %x", input, got, input)
	}
}

func TestPrecompileIdentityEmptyInput(t *testing.T) {
	c := &dataCopy{}
	got, err := c.Run(nil)
	if err != nil {
		t.Fatalf("dataCopy.Run error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("identity(nil) = %x, want empty", got)
	}
}

func TestPrecompileModexpKAT(t *testing.T) {
	// 3^5 mod 7 = 5. Each field is 1 byte: base_len=1, exp_len=1, mod_len=1.
	input := mustHex(t,
		"0000000000000000000000000000000000000000000000000000000000000001"+ // base_len
			"0000000000000000000000000000000000000000000000000000000000000001"+ // exp_len
			"0000000000000000000000000000000000000000000000000000000000000001"+ // mod_len
			"03"+ // base
			"05"+ // exp
			"07") // mod
	c := &bigModExp{eip2565: true}
	got, err := c.Run(input)
	if err != nil {
		t.Fatalf("bigModExp.Run error: %v", err)
	}
	want := []byte{0x05}
	if !bytes.Equal(got, want) {
		t.Errorf("modexp(3^5 mod 7) = %x, want %x", got, want)
	}
}

func TestPrecompileModexpZeroModulus(t *testing.T) {
	input := mustHex(t,
		"0000000000000000000000000000000000000000000000000000000000000001"+
			"0000000000000000000000000000000000000000000000000000000000000001"+
			"0000000000000000000000000000000000000000000000000000000000000001"+
			"03"+
			"05"+
			"00") // mod = 0
	c := &bigModExp{eip2565: true}
	got, err := c.Run(input)
	if err != nil {
		t.Fatalf("bigModExp.Run error: %v", err)
	}
	want := []byte{0x00}
	if !bytes.Equal(got, want) {
		t.Errorf("modexp with zero modulus = %x, want %x", got, want)
	}
}

func TestPrecompileBn256AddKAT(t *testing.T) {
	// (0,0) + (0,0) = (0,0) is a trivially valid curve-point identity case.
	input := make([]byte, 128)
	c := &bn256AddIstanbul{}
	got, err := c.Run(input)
	if err != nil {
		t.Fatalf("bn256Add.Run error: %v", err)
	}
	want := make([]byte, 64)
	if !bytes.Equal(got, want) {
		t.Errorf("bn256Add(0,0) = %x, want %x", got, want)
	}
}

func TestPrecompileBn256ScalarMulKAT(t *testing.T) {
	// 0 * (0,0) = (0,0).
	input := make([]byte, 96)
	c := &bn256ScalarMulIstanbul{}
	got, err := c.Run(input)
	if err != nil {
		t.Fatalf("bn256ScalarMul.Run error: %v", err)
	}
	want := make([]byte, 64)
	if !bytes.Equal(got, want) {
		t.Errorf("bn256ScalarMul(0,(0,0)) = %x, want %x", got, want)
	}
}

func TestPrecompileBn256PairingEmptyInputIsTrue(t *testing.T) {
	// The empty pairing check is defined to succeed (vacuously true).
	c := &bn256PairingIstanbul{}
	got, err := c.Run(nil)
	if err != nil {
		t.Fatalf("bn256Pairing.Run error: %v", err)
	}
	want := make([]byte, 32)
	want[31] = 1
	if !bytes.Equal(got, want) {
		t.Errorf("bn256Pairing(empty) = %x, want %x", got, want)
	}
}

func TestPrecompileBn256PairingInvalidLength(t *testing.T) {
	c := &bn256PairingIstanbul{}
	_, err := c.Run(make([]byte, 100)) // not a multiple of 192
	if err == nil {
		t.Fatalf("expected error for invalid pairing input length, got nil")
	}
}

func TestPrecompileBlake2FKAT(t *testing.T) {
	// A structurally valid blake2F input: rounds=12, all-zero h/m/t, and a
	// valid final-block flag. This exercises the Run() path end to end
	// (length validation, round execution, 64-byte output) without
	// depending on a hand-copied hex vector.
	h := make([]byte, 64)
	for i := range h {
		h[i] = byte(i)
	}
	m := make([]byte, 128)
	t0 := make([]byte, 8)
	t1 := make([]byte, 8)
	full := []byte{0x00, 0x00, 0x00, 0x0c}
	full = append(full, h...)
	full = append(full, m...)
	full = append(full, t0...)
	full = append(full, t1...)
	full = append(full, 0x01) // final block = true

	c := &blake2F{}
	got, err := c.Run(full)
	if err != nil {
		t.Fatalf("blake2F.Run error: %v", err)
	}
	if len(got) != 64 {
		t.Fatalf("blake2F output length = %d, want 64", len(got))
	}
}

func TestPrecompileBlake2FInvalidLength(t *testing.T) {
	c := &blake2F{}
	_, err := c.Run(make([]byte, 10))
	if err == nil {
		t.Fatalf("expected error for short blake2F input, got nil")
	}
}

func TestPrecompileBlake2FInvalidFinalFlag(t *testing.T) {
	input := make([]byte, 213)
	input[212] = 0x02 // must be 0 or 1
	c := &blake2F{}
	_, err := c.Run(input)
	if err == nil {
		t.Fatalf("expected error for invalid final-block flag, got nil")
	}
}
