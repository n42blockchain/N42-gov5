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
	"crypto/subtle"
	"errors"
	"github.com/pornin/go-fn-dsa/fndsa"
	"golang.org/x/crypto/sha3"
	"io"
)

const PublicKeySize = 897
const PrivateKeySize = 1281
const SignatureSize = 666
const SignatureAverageSize = 666
const SeedSize = 48
const N = 512
const LogN = 9
const Q = 12289

var ErrInvalidPublicKey = errors.New("falcon: invalid public key")
var ErrInvalidPrivateKey = errors.New("falcon: invalid private key")
var ErrInvalidSignature = errors.New("falcon: invalid signature")
var ErrSignatureTooLarge = errors.New("falcon: signature too large")
var ErrInvalidSeedSize = errors.New("falcon: invalid seed size")

// Original Falcon hash-to-point mode, NOT a claim of finalized FN-DSA compliance.
// Pinned fndsa v0.1.0 implements the actual NTRU equation and norm check. The
// previous local hash-commitment demonstrator is deliberately not accepted.
const originalFalcon = crypto.Hash(0xFFFFFFFF)

type PublicKey struct{ packed [PublicKeySize]byte }
type PrivateKey struct {
	packed [PrivateKeySize]byte
	pk     *PublicKey
}

func GenerateKey(random io.Reader) (*PublicKey, *PrivateKey, error) {
	if random == nil {
		random = rand.Reader
	}
	sk, pk, err := fndsa.KeyGen(9, random)
	if err != nil {
		return nil, nil, err
	}
	p := &PublicKey{}
	copy(p.packed[:], pk)
	s := &PrivateKey{pk: p}
	copy(s.packed[:], sk)
	return p, s, nil
}
func NewKeyFromSeed(seed []byte) (*PublicKey, *PrivateKey, error) {
	if len(seed) != SeedSize {
		return nil, nil, ErrInvalidSeedSize
	}
	x := sha3.NewShake256()
	x.Write([]byte("N42-Falcon512-keygen-v1"))
	x.Write(seed)
	return GenerateKey(x)
}
func Sign(sk *PrivateKey, msg []byte) ([]byte, error) { return SignWithRandom(sk, msg, rand.Reader) }
func SignWithRandom(sk *PrivateKey, msg []byte, random io.Reader) ([]byte, error) {
	if sk == nil || sk.pk == nil {
		return nil, ErrInvalidPrivateKey
	}
	if random == nil {
		random = rand.Reader
	}
	sig, err := fndsa.Sign(random, sk.packed[:], fndsa.DOMAIN_NONE, originalFalcon, msg)
	if err != nil {
		return nil, err
	}
	if !Verify(sk.pk, msg, sig) {
		return nil, ErrInvalidSignature
	}
	return sig, nil
}
func Verify(pk *PublicKey, msg, sig []byte) bool {
	return pk != nil && len(sig) == SignatureSize && fndsa.Verify(pk.packed[:], fndsa.DOMAIN_NONE, originalFalcon, msg, sig)
}
func (pk *PublicKey) Bytes() []byte   { return append([]byte(nil), pk.packed[:]...) }
func (pk *PublicKey) Pack(buf []byte) { copy(buf, pk.packed[:]) }
func (pk *PublicKey) Unpack(buf []byte) error {
	if len(buf) != PublicKeySize || buf[0] != 9 {
		return ErrInvalidPublicKey
	}
	// Canonical 14-bit coefficients, each strictly below q.
	var acc uint32
	bits := 0
	i := 1
	for j := 0; j < N; j++ {
		for bits < 14 {
			acc = (acc << 8) | uint32(buf[i])
			i++
			bits += 8
		}
		bits -= 14
		if (acc>>bits)&16383 >= Q {
			return ErrInvalidPublicKey
		}
	}
	copy(pk.packed[:], buf)
	return nil
}
func (pk *PublicKey) MarshalBinary() ([]byte, error) { return pk.Bytes(), nil }
func (pk *PublicKey) UnmarshalBinary(b []byte) error { return pk.Unpack(b) }
func (pk *PublicKey) Equal(other crypto.PublicKey) bool {
	p, ok := other.(*PublicKey)
	return ok && subtle.ConstantTimeCompare(pk.packed[:], p.packed[:]) == 1
}
func (sk *PrivateKey) Bytes() []byte   { return append([]byte(nil), sk.packed[:]...) }
func (sk *PrivateKey) Pack(buf []byte) { copy(buf, sk.packed[:]) }
func (sk *PrivateKey) Unpack(buf []byte) error {
	if len(buf) != PrivateKeySize || buf[0] != 0x59 {
		return ErrInvalidPrivateKey
	}
	p, err := publicFromPrivate(buf)
	if err != nil {
		return err
	}
	copy(sk.packed[:], buf)
	sk.pk = p
	return nil
}
func (sk *PrivateKey) MarshalBinary() ([]byte, error) { return sk.Bytes(), nil }
func (sk *PrivateKey) UnmarshalBinary(b []byte) error { return sk.Unpack(b) }
func (sk *PrivateKey) Public() crypto.PublicKey       { return sk.pk }
func (sk *PrivateKey) Sign(random io.Reader, msg []byte, opts crypto.SignerOpts) ([]byte, error) {
	if opts != nil && opts.HashFunc() != 0 {
		return nil, errors.New("falcon: raw message required")
	}
	return SignWithRandom(sk, msg, random)
}
func (sk *PrivateKey) Equal(other crypto.PrivateKey) bool {
	s, ok := other.(*PrivateKey)
	return ok && subtle.ConstantTimeCompare(sk.packed[:], s.packed[:]) == 1
}
func PublicKeyFromBytes(b []byte) (*PublicKey, error) {
	p := &PublicKey{}
	if err := p.Unpack(b); err != nil {
		return nil, err
	}
	return p, nil
}
func PrivateKeyFromBytes(b []byte) (*PrivateKey, error) {
	s := &PrivateKey{}
	if err := s.Unpack(b); err != nil {
		return nil, err
	}
	return s, nil
}
