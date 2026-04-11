// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// EncryptedEnvelope plus SealEnvelope / OpenEnvelope helpers. Uses a
// fresh ephemeral X25519 key, ECDH with the recipient, HKDF-SHA256
// key derivation and XChaCha20-Poly1305 AEAD so the 24-byte nonce is
// safe even without a session counter.

package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// EncryptedEnvelope holds an encrypted message with key exchange metadata.
type EncryptedEnvelope struct {
	EphemeralKey [32]byte // sender's ephemeral X25519 public key
	Nonce        [24]byte // XChaCha20-Poly1305 nonce
	Ciphertext   []byte   // encrypted payload + auth tag
	RecipientID  [32]byte // recipient's identity key (for routing)
}

// SealEnvelope encrypts plaintext for a specific recipient using:
// 1. Generate ephemeral X25519 key pair
// 2. ECDH with recipient's public key
// 3. HKDF to derive symmetric key
// 4. XChaCha20-Poly1305 AEAD encryption
func SealEnvelope(plaintext []byte, recipientPubKey [32]byte) (*EncryptedEnvelope, error) {
	// Generate ephemeral key pair
	var ephPriv [32]byte
	if _, err := rand.Read(ephPriv[:]); err != nil {
		return nil, fmt.Errorf("generate ephemeral key: %w", err)
	}
	ephPub, err := curve25519.X25519(ephPriv[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("compute ephemeral public key: %w", err)
	}

	// ECDH
	shared, err := curve25519.X25519(ephPriv[:], recipientPubKey[:])
	if err != nil {
		return nil, fmt.Errorf("ECDH: %w", err)
	}

	// HKDF to derive 32-byte symmetric key
	symKey, err := deriveSymmetricKey(shared, ephPub, recipientPubKey[:])
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}

	// XChaCha20-Poly1305 encrypt
	aead, err := chacha20poly1305.NewX(symKey[:])
	if err != nil {
		return nil, fmt.Errorf("create AEAD: %w", err)
	}

	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := aead.Seal(nil, nonce[:], plaintext, nil)

	env := &EncryptedEnvelope{
		Nonce:      nonce,
		Ciphertext: ciphertext,
	}
	copy(env.EphemeralKey[:], ephPub)
	copy(env.RecipientID[:], recipientPubKey[:])

	return env, nil
}

// OpenEnvelope decrypts an encrypted envelope using the recipient's private key.
func OpenEnvelope(env *EncryptedEnvelope, privateKey [32]byte) ([]byte, error) {
	if env == nil {
		return nil, errors.New("nil envelope")
	}

	// ECDH with sender's ephemeral key
	shared, err := curve25519.X25519(privateKey[:], env.EphemeralKey[:])
	if err != nil {
		return nil, fmt.Errorf("ECDH: %w", err)
	}

	// Derive the same symmetric key
	// Compute our public key for the HKDF info
	pubKey, err := curve25519.X25519(privateKey[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("compute public key: %w", err)
	}
	symKey, err := deriveSymmetricKey(shared, env.EphemeralKey[:], pubKey)
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}

	// Decrypt
	aead, err := chacha20poly1305.NewX(symKey[:])
	if err != nil {
		return nil, fmt.Errorf("create AEAD: %w", err)
	}

	plaintext, err := aead.Open(nil, env.Nonce[:], env.Ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	return plaintext, nil
}

// deriveSymmetricKey uses HKDF-SHA256 to derive a symmetric key from ECDH shared secret.
func deriveSymmetricKey(shared, ephPubKey, recipientPubKey []byte) ([32]byte, error) {
	// Info = ephemeral_pub || recipient_pub (binds to the key exchange)
	info := make([]byte, 0, len(ephPubKey)+len(recipientPubKey))
	info = append(info, ephPubKey...)
	info = append(info, recipientPubKey...)

	hkdfReader := hkdf.New(sha256.New, shared, []byte("n42-msg-e2e-v1"), info)
	var key [32]byte
	if _, err := io.ReadFull(hkdfReader, key[:]); err != nil {
		return [32]byte{}, fmt.Errorf("HKDF expand: %w", err)
	}
	return key, nil
}
