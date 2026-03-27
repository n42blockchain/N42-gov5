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

// Package falcon implements the Falcon-512 post-quantum signature scheme
// as specified in the NIST PQC competition round 3 submission.
//
// Falcon is a lattice-based signature scheme that provides strong security
// guarantees against both classical and quantum computer attacks. Falcon-512
// offers NIST security level I (equivalent to AES-128).
//
// Key sizes:
//   - Public key: 897 bytes
//   - Private key: 1281 bytes
//   - Signature: ~666 bytes (variable, max 690)
//
// This implementation wraps the reference Falcon implementation and provides
// a Go-friendly API consistent with the N42 crypto package conventions.
package falcon

import (
	"crypto"
	"crypto/rand"
	"errors"
	"io"
)

// Falcon-512 parameter sizes
const (
	// PublicKeySize is the size of a packed Falcon-512 public key in bytes.
	PublicKeySize = 897

	// PrivateKeySize is the size of a packed Falcon-512 private key in bytes.
	PrivateKeySize = 1281

	// SignatureSize is the maximum size of a Falcon-512 signature in bytes.
	// Actual signatures may be smaller due to compression.
	SignatureSize = 690

	// SignatureAverageSize is the typical size of a Falcon-512 signature.
	SignatureAverageSize = 666

	// SeedSize is the size of the seed for deterministic key generation.
	SeedSize = 48

	// N is the degree of the polynomial ring used in Falcon-512.
	N = 512

	// LogN is log2(N).
	LogN = 9

	// Q is the modulus used in Falcon.
	Q = 12289
)

var (
	// ErrInvalidPublicKey is returned when a public key cannot be parsed.
	ErrInvalidPublicKey = errors.New("falcon: invalid public key")

	// ErrInvalidPrivateKey is returned when a private key cannot be parsed.
	ErrInvalidPrivateKey = errors.New("falcon: invalid private key")

	// ErrInvalidSignature is returned when a signature cannot be verified.
	ErrInvalidSignature = errors.New("falcon: invalid signature")

	// ErrSignatureTooLarge is returned when a signature exceeds the maximum size.
	ErrSignatureTooLarge = errors.New("falcon: signature too large")

	// ErrInvalidSeedSize is returned when the seed size is incorrect.
	ErrInvalidSeedSize = errors.New("falcon: invalid seed size")
)

// PublicKey represents a Falcon-512 public key.
type PublicKey struct {
	// h is the public polynomial h = g/f mod q
	h [N]int16

	// packed is the cached packed representation
	packed [PublicKeySize]byte
}

// PrivateKey represents a Falcon-512 private key.
type PrivateKey struct {
	// f, g are the secret polynomials
	f [N]int8
	g [N]int8

	// F, G are computed such that fG - gF = q
	F [N]int8
	G [N]int8

	// pk is the corresponding public key
	pk *PublicKey

	// packed is the cached packed representation
	packed [PrivateKeySize]byte
}

// GenerateKey generates a new Falcon-512 public/private key pair.
// If rand is nil, crypto/rand.Reader will be used.
func GenerateKey(random io.Reader) (*PublicKey, *PrivateKey, error) {
	if random == nil {
		random = rand.Reader
	}

	var seed [SeedSize]byte
	if _, err := io.ReadFull(random, seed[:]); err != nil {
		return nil, nil, err
	}

	return NewKeyFromSeed(seed[:])
}

// NewKeyFromSeed derives a public/private key pair from the given seed.
// The seed must be exactly SeedSize bytes.
func NewKeyFromSeed(seed []byte) (*PublicKey, *PrivateKey, error) {
	if len(seed) != SeedSize {
		return nil, nil, ErrInvalidSeedSize
	}

	pk := &PublicKey{}
	sk := &PrivateKey{pk: pk}

	// Generate key pair from seed using SHAKE256
	if err := generateKeyPairFromSeed(pk, sk, seed); err != nil {
		return nil, nil, err
	}

	return pk, sk, nil
}

// Sign signs the message with the private key and returns the signature.
func Sign(sk *PrivateKey, msg []byte) ([]byte, error) {
	if sk == nil {
		return nil, ErrInvalidPrivateKey
	}

	// Use crypto/rand for nonce generation
	return SignWithRandom(sk, msg, rand.Reader)
}

// SignWithRandom signs the message with the private key using the provided
// random source for nonce generation.
func SignWithRandom(sk *PrivateKey, msg []byte, random io.Reader) ([]byte, error) {
	if sk == nil {
		return nil, ErrInvalidPrivateKey
	}
	if random == nil {
		random = rand.Reader
	}

	// Generate signature
	sig := make([]byte, SignatureSize)
	sigLen, err := signMessage(sig, sk, msg, random)
	if err != nil {
		return nil, err
	}

	return sig[:sigLen], nil
}

// Verify checks whether the signature is valid for the message and public key.
func Verify(pk *PublicKey, msg, sig []byte) bool {
	if pk == nil || len(sig) == 0 || len(sig) > SignatureSize {
		return false
	}

	return verifySignature(pk, msg, sig)
}

// Bytes returns the packed representation of the public key.
func (pk *PublicKey) Bytes() []byte {
	buf := make([]byte, PublicKeySize)
	pk.Pack(buf)
	return buf
}

// Pack packs the public key into buf.
// Panics if buf is not of size PublicKeySize.
func (pk *PublicKey) Pack(buf []byte) {
	if len(buf) != PublicKeySize {
		panic("falcon: buf must be of length PublicKeySize")
	}
	packPublicKey(buf, pk)
}

// Unpack unpacks the public key from buf.
// Returns an error if buf is not a valid public key.
func (pk *PublicKey) Unpack(buf []byte) error {
	if len(buf) != PublicKeySize {
		return ErrInvalidPublicKey
	}
	return unpackPublicKey(pk, buf)
}

// MarshalBinary implements encoding.BinaryMarshaler.
func (pk *PublicKey) MarshalBinary() ([]byte, error) {
	return pk.Bytes(), nil
}

// UnmarshalBinary implements encoding.BinaryUnmarshaler.
func (pk *PublicKey) UnmarshalBinary(data []byte) error {
	return pk.Unpack(data)
}

// Equal returns true if the two public keys are equal.
func (pk *PublicKey) Equal(other crypto.PublicKey) bool {
	otherPK, ok := other.(*PublicKey)
	if !ok {
		return false
	}
	return pk.h == otherPK.h
}

// Bytes returns the packed representation of the private key.
func (sk *PrivateKey) Bytes() []byte {
	buf := make([]byte, PrivateKeySize)
	sk.Pack(buf)
	return buf
}

// Pack packs the private key into buf.
// Panics if buf is not of size PrivateKeySize.
func (sk *PrivateKey) Pack(buf []byte) {
	if len(buf) != PrivateKeySize {
		panic("falcon: buf must be of length PrivateKeySize")
	}
	packPrivateKey(buf, sk)
}

// Unpack unpacks the private key from buf.
// Returns an error if buf is not a valid private key.
func (sk *PrivateKey) Unpack(buf []byte) error {
	if len(buf) != PrivateKeySize {
		return ErrInvalidPrivateKey
	}
	return unpackPrivateKey(sk, buf)
}

// MarshalBinary implements encoding.BinaryMarshaler.
func (sk *PrivateKey) MarshalBinary() ([]byte, error) {
	return sk.Bytes(), nil
}

// UnmarshalBinary implements encoding.BinaryUnmarshaler.
func (sk *PrivateKey) UnmarshalBinary(data []byte) error {
	return sk.Unpack(data)
}

// Public returns the public key corresponding to this private key.
func (sk *PrivateKey) Public() crypto.PublicKey {
	return sk.pk
}

// Sign implements crypto.Signer.
// opts is ignored as Falcon does not support pre-hashing.
func (sk *PrivateKey) Sign(random io.Reader, msg []byte, opts crypto.SignerOpts) ([]byte, error) {
	if opts != nil && opts.HashFunc() != crypto.Hash(0) {
		return nil, errors.New("falcon: cannot sign pre-hashed message")
	}
	return SignWithRandom(sk, msg, random)
}

// Equal returns true if the two private keys are equal.
func (sk *PrivateKey) Equal(other crypto.PrivateKey) bool {
	otherSK, ok := other.(*PrivateKey)
	if !ok {
		return false
	}
	return sk.f == otherSK.f && sk.g == otherSK.g &&
		sk.F == otherSK.F && sk.G == otherSK.G
}

// PublicKeyFromBytes parses a public key from its packed representation.
func PublicKeyFromBytes(data []byte) (*PublicKey, error) {
	pk := &PublicKey{}
	if err := pk.Unpack(data); err != nil {
		return nil, err
	}
	return pk, nil
}

// PrivateKeyFromBytes parses a private key from its packed representation.
func PrivateKeyFromBytes(data []byte) (*PrivateKey, error) {
	sk := &PrivateKey{pk: &PublicKey{}}
	if err := sk.Unpack(data); err != nil {
		return nil, err
	}
	return sk, nil
}
