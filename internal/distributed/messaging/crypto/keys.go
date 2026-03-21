// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package crypto provides end-to-end encryption for the messaging relay service.
// Uses X25519 ECDH + HKDF + ChaCha20-Poly1305 for message encryption.
package crypto

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"

	ncrypto "github.com/n42blockchain/N42/common/crypto"
)

// MessagingKeyPair holds an X25519 key pair for ECDH.
type MessagingKeyPair struct {
	PrivateKey [32]byte
	PublicKey  [32]byte
}

// GenerateKeyPair generates a new random X25519 key pair.
func GenerateKeyPair() (*MessagingKeyPair, error) {
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return nil, fmt.Errorf("generate key pair: %w", err)
	}
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("compute public key: %w", err)
	}
	kp := &MessagingKeyPair{}
	copy(kp.PrivateKey[:], priv[:])
	copy(kp.PublicKey[:], pub)
	return kp, nil
}

// DeriveFromWallet derives an X25519 messaging key pair from a secp256k1 wallet private key.
// Uses HKDF to derive deterministic X25519 key material from the wallet key.
func DeriveFromWallet(walletKey *ecdsa.PrivateKey) (*MessagingKeyPair, error) {
	if walletKey == nil {
		return nil, errors.New("nil wallet key")
	}
	// Use the raw secp256k1 private key bytes as IKM
	ikm := ncrypto.FromECDSA(walletKey)
	if len(ikm) == 0 {
		return nil, errors.New("empty wallet key")
	}

	// HKDF-SHA256 to derive X25519 key material
	hkdfReader := hkdf.New(sha256.New, ikm, []byte("n42-messaging-x25519"), []byte("key-derivation-v1"))
	var priv [32]byte
	if _, err := io.ReadFull(hkdfReader, priv[:]); err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}

	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("compute public key: %w", err)
	}
	kp := &MessagingKeyPair{}
	copy(kp.PrivateKey[:], priv[:])
	copy(kp.PublicKey[:], pub)
	return kp, nil
}

// KeyBundle contains identity key + signed prekey + one-time prekeys.
type KeyBundle struct {
	IdentityKey  [32]byte   // Long-term X25519 identity key (public)
	SignedPreKey [32]byte   // Medium-term signed prekey (public)
	OneTimeKeys  [][32]byte // One-time prekeys for initial key exchange
}

// KeyStore manages local and known peer keys.
type KeyStore struct {
	mu       sync.RWMutex
	localKey *MessagingKeyPair
	peerKeys map[string]*KeyBundle // keyed by hex-encoded identity key
}

// NewKeyStore creates a new key store.
func NewKeyStore(localKey *MessagingKeyPair) *KeyStore {
	return &KeyStore{
		localKey: localKey,
		peerKeys: make(map[string]*KeyBundle),
	}
}

// LocalKey returns the local messaging key pair.
func (ks *KeyStore) LocalKey() *MessagingKeyPair {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return ks.localKey
}

// AddPeerBundle stores a peer's key bundle.
func (ks *KeyStore) AddPeerBundle(peerID string, bundle *KeyBundle) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.peerKeys[peerID] = bundle
}

// GetPeerBundle retrieves a peer's key bundle.
func (ks *KeyStore) GetPeerBundle(peerID string) (*KeyBundle, bool) {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	b, ok := ks.peerKeys[peerID]
	return b, ok
}

// RemovePeerBundle removes a peer's key bundle.
func (ks *KeyStore) RemovePeerBundle(peerID string) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	delete(ks.peerKeys, peerID)
}

// ECDH performs X25519 Diffie-Hellman key agreement.
func ECDH(privateKey [32]byte, publicKey [32]byte) ([32]byte, error) {
	shared, err := curve25519.X25519(privateKey[:], publicKey[:])
	if err != nil {
		return [32]byte{}, fmt.Errorf("ECDH: %w", err)
	}
	var result [32]byte
	copy(result[:], shared)
	return result, nil
}
