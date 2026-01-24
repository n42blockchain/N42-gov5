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

//go:build !nacl && !js && cgo && !gofuzz

package crypto

import (
	"bytes"
	"testing"
)

func TestSign(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	// Create a digest hash (must be exactly 32 bytes)
	digest := Keccak256([]byte("test message"))

	sig, err := Sign(digest, key)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	// Signature should be 65 bytes (R + S + V)
	if len(sig) != SignatureLength {
		t.Errorf("Signature length = %d, want %d", len(sig), SignatureLength)
	}

	// Verify the signature
	pubBytes := FromECDSAPub(&key.PublicKey)
	if !VerifySignature(pubBytes, digest, sig[:64]) {
		t.Error("VerifySignature failed for valid signature")
	}
}

func TestSignInvalidDigestLength(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	// Test with wrong digest length
	tests := []struct {
		name   string
		digest []byte
	}{
		{"too short", make([]byte, 31)},
		{"too long", make([]byte, 33)},
		{"empty", []byte{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Sign(tt.digest, key)
			if err == nil {
				t.Error("Sign() should fail with invalid digest length")
			}
		})
	}
}

func TestEcrecover(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	digest := Keccak256([]byte("test message"))

	sig, err := Sign(digest, key)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	// Recover public key from signature
	recoveredPub, err := Ecrecover(digest, sig)
	if err != nil {
		t.Fatalf("Ecrecover() error = %v", err)
	}

	// Compare with original public key
	originalPub := FromECDSAPub(&key.PublicKey)
	if !bytes.Equal(recoveredPub, originalPub) {
		t.Error("Ecrecover didn't recover the correct public key")
	}
}

func TestSigToPub(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	digest := Keccak256([]byte("test message"))

	sig, err := Sign(digest, key)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	// Recover public key
	recoveredPub, err := SigToPub(digest, sig)
	if err != nil {
		t.Fatalf("SigToPub() error = %v", err)
	}

	// Compare X and Y coordinates
	if recoveredPub.X.Cmp(key.PublicKey.X) != 0 || recoveredPub.Y.Cmp(key.PublicKey.Y) != 0 {
		t.Error("SigToPub didn't recover the correct public key")
	}
}

func TestVerifySignature(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	digest := Keccak256([]byte("test message"))

	sig, err := Sign(digest, key)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	// Get public key in both formats
	pubUncompressed := FromECDSAPub(&key.PublicKey)
	pubCompressed := CompressPubkey(&key.PublicKey)

	// Verify with uncompressed public key (65 bytes)
	if !VerifySignature(pubUncompressed, digest, sig[:64]) {
		t.Error("VerifySignature failed with uncompressed public key")
	}

	// Verify with compressed public key (33 bytes)
	if !VerifySignature(pubCompressed, digest, sig[:64]) {
		t.Error("VerifySignature failed with compressed public key")
	}

	// Test with wrong digest
	wrongDigest := Keccak256([]byte("wrong message"))
	if VerifySignature(pubUncompressed, wrongDigest, sig[:64]) {
		t.Error("VerifySignature should fail with wrong digest")
	}

	// Test with corrupted signature
	corruptedSig := make([]byte, 64)
	copy(corruptedSig, sig[:64])
	corruptedSig[0] ^= 0xFF
	if VerifySignature(pubUncompressed, digest, corruptedSig) {
		t.Error("VerifySignature should fail with corrupted signature")
	}
}

func TestCompressDecompressPubkey(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	// Compress the public key
	compressed := CompressPubkey(&key.PublicKey)
	if len(compressed) != 33 {
		t.Errorf("Compressed public key length = %d, want 33", len(compressed))
	}

	// Decompress and verify
	decompressed, err := DecompressPubkey(compressed)
	if err != nil {
		t.Fatalf("DecompressPubkey() error = %v", err)
	}

	if decompressed.X.Cmp(key.PublicKey.X) != 0 || decompressed.Y.Cmp(key.PublicKey.Y) != 0 {
		t.Error("DecompressPubkey didn't recover the correct public key")
	}
}

func TestDecompressPubkeyInvalid(t *testing.T) {
	// Test with invalid input
	_, err := DecompressPubkey([]byte{1, 2, 3})
	if err == nil {
		t.Error("DecompressPubkey should fail with invalid input")
	}
}

func TestS256(t *testing.T) {
	curve := S256()
	if curve == nil {
		t.Fatal("S256() returned nil")
	}

	// Verify curve parameters are set correctly (BitSize for secp256k1 is 256)
	params := curve.Params()
	if params.BitSize != 256 {
		t.Errorf("Curve BitSize = %d, want 256", params.BitSize)
	}
}

func TestSignatureRoundTrip(t *testing.T) {
	// Test end-to-end signing and verification
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	messages := []string{
		"hello world",
		"",
		"a",
		"The quick brown fox jumps over the lazy dog",
	}

	for _, msg := range messages {
		t.Run(msg, func(t *testing.T) {
			digest := Keccak256([]byte(msg))

			sig, err := Sign(digest, key)
			if err != nil {
				t.Fatalf("Sign() error = %v", err)
			}

			// Recover public key
			recoveredPub, err := SigToPub(digest, sig)
			if err != nil {
				t.Fatalf("SigToPub() error = %v", err)
			}

			// Verify recovered address matches original
			originalAddr := PubkeyToAddress(key.PublicKey)
			recoveredAddr := PubkeyToAddress(*recoveredPub)

			if originalAddr != recoveredAddr {
				t.Error("Recovered address doesn't match original")
			}
		})
	}
}
