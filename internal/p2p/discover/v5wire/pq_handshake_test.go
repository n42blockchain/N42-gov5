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

package v5wire

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/n42blockchain/N42/common/crypto/kem/kyber/kyber768"
)

func TestNewHybridHandshake(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		h, err := NewHybridHandshake(true)
		if err != nil {
			t.Fatalf("NewHybridHandshake(true) failed: %v", err)
		}
		if !h.IsEnabled() {
			t.Error("expected handshake to be enabled")
		}
		if h.ECDHPublicKey() == nil {
			t.Error("ECDH public key should not be nil")
		}
		if h.KyberPublicKey() == nil {
			t.Error("Kyber public key should not be nil")
		}
		if len(h.KyberPublicKey()) != KyberPublicKeySize {
			t.Errorf("Kyber public key size = %d, want %d", len(h.KyberPublicKey()), KyberPublicKeySize)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		h, err := NewHybridHandshake(false)
		if err != nil {
			t.Fatalf("NewHybridHandshake(false) failed: %v", err)
		}
		if h.IsEnabled() {
			t.Error("expected handshake to be disabled")
		}
	})
}

func TestHybridHandshakeKeyExchange(t *testing.T) {
	alice, err := NewHybridHandshake(true)
	if err != nil {
		t.Fatalf("failed to create Alice handshake: %v", err)
	}

	bob, err := NewHybridHandshake(true)
	if err != nil {
		t.Fatalf("failed to create Bob handshake: %v", err)
	}

	alicePubKey := &HybridPublicKey{
		ECDH:  alice.ECDHPublicKey(),
		Kyber: alice.kyberPK,
	}
	bobPubKey := &HybridPublicKey{
		ECDH:  bob.ECDHPublicKey(),
		Kyber: bob.kyberPK,
	}

	// Alice encapsulates to Bob, Bob decapsulates.
	ciphertext, aliceSecret, err := alice.Encapsulate(bobPubKey)
	if err != nil {
		t.Fatalf("Alice encapsulation failed: %v", err)
	}
	if len(ciphertext) != ecdhUncompressedPubLen+KyberCiphertextSize {
		t.Errorf("ciphertext size = %d, want %d", len(ciphertext), ecdhUncompressedPubLen+KyberCiphertextSize)
	}
	if len(aliceSecret) != 32 {
		t.Errorf("shared secret size = %d, want 32", len(aliceSecret))
	}

	bobSecret, err := bob.Decapsulate(ciphertext)
	if err != nil {
		t.Fatalf("Bob decapsulation failed: %v", err)
	}
	if !bytes.Equal(aliceSecret, bobSecret) {
		t.Errorf("shared secrets mismatch:\n  alice: %x\n  bob:   %x", aliceSecret, bobSecret)
	}

	// Reverse direction: Bob encapsulates to Alice, Alice decapsulates.
	ciphertext2, bobSecret2, err := bob.Encapsulate(alicePubKey)
	if err != nil {
		t.Fatalf("Bob encapsulation failed: %v", err)
	}

	aliceSecret2, err := alice.Decapsulate(ciphertext2)
	if err != nil {
		t.Fatalf("Alice decapsulation failed: %v", err)
	}
	if !bytes.Equal(aliceSecret2, bobSecret2) {
		t.Error("reverse key exchange: shared secrets mismatch")
	}
}

func TestHybridHandshakeDisabled(t *testing.T) {
	h, _ := NewHybridHandshake(false)

	_, _, err := h.Encapsulate(nil)
	if err != ErrHybridHandshakeDisabled {
		t.Errorf("Encapsulate: got %v, want ErrHybridHandshakeDisabled", err)
	}

	_, err = h.Decapsulate(nil)
	if err != ErrHybridHandshakeDisabled {
		t.Errorf("Decapsulate: got %v, want ErrHybridHandshakeDisabled", err)
	}
}

func TestHybridHandshakeInvalidCiphertext(t *testing.T) {
	h, _ := NewHybridHandshake(true)

	_, err := h.Decapsulate([]byte{1, 2, 3})
	if err != ErrCiphertextTooShort {
		t.Errorf("got %v, want ErrCiphertextTooShort", err)
	}
}

func TestKyberKeyGeneration(t *testing.T) {
	pk, sk, err := GenerateKyberKeyPair(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKyberKeyPair failed: %v", err)
	}
	if pk == nil || sk == nil {
		t.Fatal("keys should not be nil")
	}

	// Round-trip: pack then parse the public key.
	pkBytes := make([]byte, KyberPublicKeySize)
	pk.Pack(pkBytes)

	pk2, err := ParseKyberPublicKey(pkBytes)
	if err != nil {
		t.Fatalf("ParseKyberPublicKey failed: %v", err)
	}

	// Encapsulate with the parsed key, decapsulate with the original secret key.
	ct, ss1, err := KyberEncapsulate(pk2)
	if err != nil {
		t.Fatalf("KyberEncapsulate failed: %v", err)
	}

	ss2, err := KyberDecapsulate(sk, ct)
	if err != nil {
		t.Fatalf("KyberDecapsulate failed: %v", err)
	}

	if !bytes.Equal(ss1, ss2) {
		t.Error("Kyber shared secrets mismatch after public key round-trip")
	}
}

func TestKyberEncapsulateDecapsulate(t *testing.T) {
	pk, sk, _ := kyber768.GenerateKeyPair(rand.Reader)

	ct, ss1, err := KyberEncapsulate(pk)
	if err != nil {
		t.Fatalf("KyberEncapsulate failed: %v", err)
	}
	if len(ct) != KyberCiphertextSize {
		t.Errorf("ciphertext size = %d, want %d", len(ct), KyberCiphertextSize)
	}
	if len(ss1) != KyberSharedKeySize {
		t.Errorf("shared secret size = %d, want %d", len(ss1), KyberSharedKeySize)
	}

	ss2, err := KyberDecapsulate(sk, ct)
	if err != nil {
		t.Fatalf("KyberDecapsulate failed: %v", err)
	}
	if !bytes.Equal(ss1, ss2) {
		t.Error("shared secrets mismatch")
	}
}

func TestKyberDecapsulateInvalidCiphertext(t *testing.T) {
	_, sk, _ := kyber768.GenerateKeyPair(rand.Reader)

	_, err := KyberDecapsulate(sk, []byte{1, 2, 3})
	if err != ErrInvalidKyberCiphertext {
		t.Errorf("got %v, want ErrInvalidKyberCiphertext", err)
	}
}

func TestParseKyberPublicKeyInvalid(t *testing.T) {
	_, err := ParseKyberPublicKey([]byte{1, 2, 3})
	if err != ErrInvalidKyberPublicKey {
		t.Errorf("got %v, want ErrInvalidKyberPublicKey", err)
	}
}

func TestDefaultHybridConfig(t *testing.T) {
	cfg := DefaultHybridConfig()

	if !cfg.Enabled {
		t.Error("default config should have Enabled=true")
	}
	if !cfg.FallbackToECDH {
		t.Error("default config should have FallbackToECDH=true")
	}
}

func TestHybridConstants(t *testing.T) {
	if KyberPublicKeySize != 1184 {
		t.Errorf("KyberPublicKeySize = %d, want 1184", KyberPublicKeySize)
	}
	if KyberCiphertextSize != 1088 {
		t.Errorf("KyberCiphertextSize = %d, want 1088", KyberCiphertextSize)
	}
	if KyberSharedKeySize != 32 {
		t.Errorf("KyberSharedKeySize = %d, want 32", KyberSharedKeySize)
	}
}

func BenchmarkHybridKeyExchange(b *testing.B) {
	alice, _ := NewHybridHandshake(true)
	bob, _ := NewHybridHandshake(true)

	bobPubKey := &HybridPublicKey{
		ECDH:  bob.ECDHPublicKey(),
		Kyber: bob.kyberPK,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ct, _, _ := alice.Encapsulate(bobPubKey)
		bob.Decapsulate(ct)
	}
}

func BenchmarkKyberEncapsulate(b *testing.B) {
	pk, _, _ := kyber768.GenerateKeyPair(rand.Reader)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		KyberEncapsulate(pk)
	}
}

func BenchmarkKyberDecapsulate(b *testing.B) {
	pk, sk, _ := kyber768.GenerateKeyPair(rand.Reader)
	ct, _, _ := KyberEncapsulate(pk)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		KyberDecapsulate(sk, ct)
	}
}
