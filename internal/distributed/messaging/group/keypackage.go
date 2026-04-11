// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// MLS KeyPackage construction. CipherSuiteID selects
// MLS_128_HPKEX25519_CHACHA20POLY1305_SHA256_Ed25519 and
// DefaultKeyPackageLifetime sets a 30-day expiry. CreateKeyPackage
// generates the HPKE init key and signs the credential for group add.

package group

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"
)

const (
	// CipherSuiteID identifies the cryptographic algorithms used.
	// MLS_128_HPKEX25519_CHACHA20POLY1305_SHA256_Ed25519
	CipherSuiteID = 0x0003

	// DefaultKeyPackageLifetime is the KeyPackage validity duration.
	DefaultKeyPackageLifetime = 30 * 24 * time.Hour // 30 days
)

// KeyPackage is an MLS KeyPackage containing everything needed to add a member.
type KeyPackage struct {
	Version     uint8
	CipherSuite uint16
	InitKey     [32]byte // HPKE init key (X25519 public key)
	Credential  []byte   // identity credential (address, DID, etc.)
	Signature   []byte   // signature over the package
	ExpiresAt   int64    // unix timestamp
}

// CreateKeyPackage creates a new KeyPackage for group membership.
func CreateKeyPackage(identityKey [32]byte, credential []byte) (*KeyPackage, *KeyPair, error) {
	// Generate init key pair
	initKP, err := GenerateKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("generate init key: %w", err)
	}

	pkg := &KeyPackage{
		Version:     1,
		CipherSuite: CipherSuiteID,
		InitKey:     initKP.PublicKey,
		Credential:  credential,
		ExpiresAt:   time.Now().Add(DefaultKeyPackageLifetime).Unix(),
	}

	// Sign the key package
	pkg.Signature = signKeyPackage(pkg, identityKey)

	return pkg, initKP, nil
}

// ValidateKeyPackage checks that a KeyPackage is well-formed and not expired.
func ValidateKeyPackage(pkg *KeyPackage) error {
	if pkg == nil {
		return errors.New("nil key package")
	}
	if pkg.Version != 1 {
		return fmt.Errorf("unsupported version: %d", pkg.Version)
	}
	if pkg.CipherSuite != CipherSuiteID {
		return fmt.Errorf("unsupported cipher suite: %d", pkg.CipherSuite)
	}
	if pkg.InitKey == ([32]byte{}) {
		return errors.New("empty init key")
	}
	if time.Now().Unix() > pkg.ExpiresAt {
		return errors.New("key package expired")
	}
	if len(pkg.Signature) == 0 {
		return errors.New("missing signature")
	}
	return nil
}

// signKeyPackage creates a signature over the key package contents.
func signKeyPackage(pkg *KeyPackage, signingKey [32]byte) []byte {
	h := sha256.New()
	h.Write([]byte{pkg.Version})
	h.Write([]byte{byte(pkg.CipherSuite >> 8), byte(pkg.CipherSuite)})
	h.Write(pkg.InitKey[:])
	h.Write(pkg.Credential)
	digest := h.Sum(nil)

	// HMAC-style signature using signing key
	sig := sha256.New()
	sig.Write(signingKey[:])
	sig.Write(digest)

	// Add randomness to prevent signature reuse analysis
	var nonce [16]byte
	rand.Read(nonce[:])
	sig.Write(nonce[:])

	result := make([]byte, 0, 32+16)
	result = append(result, sig.Sum(nil)...)
	result = append(result, nonce[:]...)
	return result
}
