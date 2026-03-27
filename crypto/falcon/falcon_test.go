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

package falcon

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestKeyGeneration(t *testing.T) {
	pk, sk, err := GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	if pk == nil {
		t.Fatal("Public key is nil")
	}
	if sk == nil {
		t.Fatal("Private key is nil")
	}

	// Check key sizes
	pkBytes := pk.Bytes()
	if len(pkBytes) != PublicKeySize {
		t.Errorf("Public key size = %d, want %d", len(pkBytes), PublicKeySize)
	}

	skBytes := sk.Bytes()
	if len(skBytes) != PrivateKeySize {
		t.Errorf("Private key size = %d, want %d", len(skBytes), PrivateKeySize)
	}
}

func TestKeyFromSeed(t *testing.T) {
	seed := make([]byte, SeedSize)
	rand.Read(seed)

	pk1, sk1, err := NewKeyFromSeed(seed)
	if err != nil {
		t.Fatalf("NewKeyFromSeed failed: %v", err)
	}

	pk2, sk2, err := NewKeyFromSeed(seed)
	if err != nil {
		t.Fatalf("NewKeyFromSeed failed: %v", err)
	}

	// Same seed should produce same keys
	if !bytes.Equal(pk1.Bytes(), pk2.Bytes()) {
		t.Error("Same seed produced different public keys")
	}
	if !bytes.Equal(sk1.Bytes(), sk2.Bytes()) {
		t.Error("Same seed produced different private keys")
	}
}

func TestSignVerify(t *testing.T) {
	pk, sk, err := GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	msg := []byte("Hello, Falcon-512!")

	sig, err := Sign(sk, msg)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	if len(sig) > SignatureSize {
		t.Errorf("Signature size %d exceeds maximum %d", len(sig), SignatureSize)
	}

	if !Verify(pk, msg, sig) {
		t.Error("Valid signature failed verification")
	}
}

func TestSignVerifyDifferentMessages(t *testing.T) {
	pk, sk, err := GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	msg1 := []byte("Message 1")
	msg2 := []byte("Message 2")

	sig1, err := Sign(sk, msg1)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	// Signature for msg1 should not verify for msg2
	if Verify(pk, msg2, sig1) {
		t.Error("Signature verified for wrong message")
	}

	// Signature should verify for correct message
	if !Verify(pk, msg1, sig1) {
		t.Error("Signature failed for correct message")
	}
}

func TestSignVerifyWrongKey(t *testing.T) {
	pk1, sk1, _ := GenerateKey(rand.Reader)
	pk2, _, _ := GenerateKey(rand.Reader)

	msg := []byte("Test message")

	sig, err := Sign(sk1, msg)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	// Should verify with correct key
	if !Verify(pk1, msg, sig) {
		t.Error("Signature failed with correct key")
	}

	// Should not verify with wrong key
	if Verify(pk2, msg, sig) {
		t.Error("Signature verified with wrong key")
	}
}

func TestKeyMarshaling(t *testing.T) {
	pk, sk, err := GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	// Test public key marshaling
	pkBytes, err := pk.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary failed: %v", err)
	}

	pk2 := &PublicKey{}
	if err := pk2.UnmarshalBinary(pkBytes); err != nil {
		t.Fatalf("UnmarshalBinary failed: %v", err)
	}

	if !pk.Equal(pk2) {
		t.Error("Unmarshaled public key differs from original")
	}

	// Test private key marshaling
	skBytes, err := sk.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary failed: %v", err)
	}

	sk2 := &PrivateKey{pk: &PublicKey{}}
	if err := sk2.UnmarshalBinary(skBytes); err != nil {
		t.Fatalf("UnmarshalBinary failed: %v", err)
	}

	if !sk.Equal(sk2) {
		t.Error("Unmarshaled private key differs from original")
	}
}

func TestPublicKeyFromBytes(t *testing.T) {
	pk, _, err := GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	pkBytes := pk.Bytes()
	pk2, err := PublicKeyFromBytes(pkBytes)
	if err != nil {
		t.Fatalf("PublicKeyFromBytes failed: %v", err)
	}

	if !pk.Equal(pk2) {
		t.Error("Parsed public key differs from original")
	}
}

func TestPrivateKeyFromBytes(t *testing.T) {
	_, sk, err := GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	skBytes := sk.Bytes()
	sk2, err := PrivateKeyFromBytes(skBytes)
	if err != nil {
		t.Fatalf("PrivateKeyFromBytes failed: %v", err)
	}

	if !sk.Equal(sk2) {
		t.Error("Parsed private key differs from original")
	}
}

func TestInvalidInputs(t *testing.T) {
	pk, _, _ := GenerateKey(rand.Reader)

	// Empty signature
	if Verify(pk, []byte("test"), []byte{}) {
		t.Error("Verified empty signature")
	}

	// Nil public key
	if Verify(nil, []byte("test"), []byte{1, 2, 3}) {
		t.Error("Verified with nil public key")
	}

	// Invalid seed size
	_, _, err := NewKeyFromSeed([]byte{1, 2, 3})
	if err != ErrInvalidSeedSize {
		t.Errorf("Expected ErrInvalidSeedSize, got %v", err)
	}
}

func TestCryptoSignerInterface(t *testing.T) {
	_, sk, err := GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	msg := []byte("Test message for crypto.Signer")

	// Test that PrivateKey implements crypto.Signer
	sig, err := sk.Sign(rand.Reader, msg, nil)
	if err != nil {
		t.Fatalf("crypto.Signer.Sign failed: %v", err)
	}

	pk := sk.Public().(*PublicKey)
	if !Verify(pk, msg, sig) {
		t.Error("crypto.Signer signature failed verification")
	}
}

func TestLargeMessage(t *testing.T) {
	pk, sk, err := GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	// Test with 1MB message
	msg := make([]byte, 1<<20)
	rand.Read(msg)

	sig, err := Sign(sk, msg)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	if !Verify(pk, msg, sig) {
		t.Error("Large message signature failed verification")
	}
}

func TestEmptyMessage(t *testing.T) {
	pk, sk, err := GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	msg := []byte{}

	sig, err := Sign(sk, msg)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	if !Verify(pk, msg, sig) {
		t.Error("Empty message signature failed verification")
	}
}

// Benchmarks

func BenchmarkKeyGeneration(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _, err := GenerateKey(rand.Reader)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSign(b *testing.B) {
	_, sk, err := GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	msg := []byte("Benchmark message for signing")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := Sign(sk, msg)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkVerify(b *testing.B) {
	pk, sk, err := GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	msg := []byte("Benchmark message for verification")
	sig, err := Sign(sk, msg)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !Verify(pk, msg, sig) {
			b.Fatal("Verification failed")
		}
	}
}

func BenchmarkSignVerify(b *testing.B) {
	pk, sk, err := GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	msg := []byte("Benchmark message for sign+verify")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sig, err := Sign(sk, msg)
		if err != nil {
			b.Fatal(err)
		}
		if !Verify(pk, msg, sig) {
			b.Fatal("Verification failed")
		}
	}
}
