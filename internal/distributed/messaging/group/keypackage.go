// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// MLS KeyPackage construction. CipherSuiteID selects
// MLS_128_HPKEX25519_CHACHA20POLY1305_SHA256_Ed25519 and
// DefaultKeyPackageLifetime sets a 30-day expiry. CreateKeyPackage
// generates the HPKE init key and signs the package with the member's
// Ed25519 identity key; ValidateKeyPackage verifies that signature.

package group

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
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
	SigningKey  [32]byte // Ed25519 identity key; commits from this member verify against it
	Credential  []byte   // identity credential (address, DID, etc.)
	Signature   []byte   // Ed25519 signature by SigningKey over keyPackageDigest
	ExpiresAt   int64    // unix timestamp
}

// CreateKeyPackage creates a new KeyPackage for group membership, signed with
// the member's Ed25519 identity seed (KeyPair.PrivateKey layout).
func CreateKeyPackage(identitySeed [32]byte, credential []byte) (*KeyPackage, *KeyPair, error) {
	// Generate init key pair
	initKP, err := GenerateKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("generate init key: %w", err)
	}

	signer := ed25519.NewKeyFromSeed(identitySeed[:])
	pkg := &KeyPackage{
		Version:     1,
		CipherSuite: CipherSuiteID,
		InitKey:     initKP.PublicKey,
		Credential:  credential,
		ExpiresAt:   time.Now().Add(DefaultKeyPackageLifetime).Unix(),
	}
	copy(pkg.SigningKey[:], signer.Public().(ed25519.PublicKey))
	pkg.Signature = ed25519.Sign(signer, keyPackageDigest(pkg))

	return pkg, initKP, nil
}

// ValidateKeyPackage checks that a KeyPackage is well-formed, not expired,
// and carries a valid self-signature by its embedded identity key.
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
	if pkg.SigningKey == ([32]byte{}) {
		return errors.New("empty signing key")
	}
	if time.Now().Unix() > pkg.ExpiresAt {
		return errors.New("key package expired")
	}
	if !ed25519.Verify(pkg.SigningKey[:], keyPackageDigest(pkg), pkg.Signature) {
		return errors.New("key package signature verification failed")
	}
	return nil
}

// keyPackageDigest is the canonical signing input: every field except the
// signature, including the expiry (an unsigned expiry is an attacker-chosen
// expiry).
func keyPackageDigest(pkg *KeyPackage) []byte {
	h := sha256.New()
	h.Write([]byte{pkg.Version})
	h.Write([]byte{byte(pkg.CipherSuite >> 8), byte(pkg.CipherSuite)})
	h.Write(pkg.InitKey[:])
	h.Write(pkg.SigningKey[:])
	h.Write(pkg.Credential)
	var exp [8]byte
	binary.BigEndian.PutUint64(exp[:], uint64(pkg.ExpiresAt))
	h.Write(exp[:])
	return h.Sum(nil)
}
