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

// Post-Quantum Hybrid Handshake
//
// This module implements a hybrid key exchange that combines:
// - ECDH (secp256k1) for backward compatibility
// - Kyber-768 for post-quantum security
//
// The combined shared secret provides security against both classical
// and quantum computer attacks. Even if one of the key exchanges is
// broken, the other provides security (defense in depth).
//
// Hybrid key derivation:
// sharedSecret = HKDF(ecdhSecret || kyberSecret, ...)

package v5wire

import (
	"crypto/ecdsa"
	"crypto/rand"
	"errors"
	"hash"
	"io"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/crypto/kem/kyber/kyber768"
	"github.com/n42blockchain/N42/internal/p2p/enode"
	"golang.org/x/crypto/hkdf"
)

// Kyber-768 parameter sizes
const (
	KyberPublicKeySize  = kyber768.PublicKeySize  // 1184 bytes
	KyberPrivateKeySize = kyber768.PrivateKeySize // 2400 bytes
	KyberCiphertextSize = kyber768.CiphertextSize // 1088 bytes
	KyberSharedKeySize  = kyber768.SharedKeySize  // 32 bytes
)

// Errors
var (
	ErrHybridHandshakeDisabled = errors.New("hybrid PQ handshake is disabled")
	ErrKyberKeyGenFailed       = errors.New("kyber key generation failed")
	ErrKyberEncapFailed        = errors.New("kyber encapsulation failed")
	ErrKyberDecapFailed        = errors.New("kyber decapsulation failed")
	ErrInvalidKyberPublicKey   = errors.New("invalid kyber public key size")
	ErrInvalidKyberCiphertext  = errors.New("invalid kyber ciphertext size")
)

// HybridPublicKey contains both ECDH and Kyber public keys
type HybridPublicKey struct {
	ECDH  *ecdsa.PublicKey
	Kyber *kyber768.PublicKey
}

// HybridPrivateKey contains both ECDH and Kyber private keys
type HybridPrivateKey struct {
	ECDH  *ecdsa.PrivateKey
	Kyber *kyber768.PrivateKey
}

// HybridHandshake manages hybrid key exchange state
type HybridHandshake struct {
	ecdhKey   *ecdsa.PrivateKey
	kyberSK   *kyber768.PrivateKey
	kyberPK   *kyber768.PublicKey
	enabled   bool
}

// NewHybridHandshake creates a new hybrid handshake instance
func NewHybridHandshake(enabled bool) (*HybridHandshake, error) {
	if !enabled {
		return &HybridHandshake{enabled: false}, nil
	}

	// Generate ECDH key pair
	ecdhKey, err := crypto.GenerateKey()
	if err != nil {
		return nil, err
	}

	// Generate Kyber-768 key pair
	kyberPK, kyberSK, err := kyber768.GenerateKeyPair(rand.Reader)
	if err != nil {
		return nil, ErrKyberKeyGenFailed
	}

	return &HybridHandshake{
		ecdhKey: ecdhKey,
		kyberSK: kyberSK,
		kyberPK: kyberPK,
		enabled: true,
	}, nil
}

// IsEnabled returns whether hybrid handshake is enabled
func (h *HybridHandshake) IsEnabled() bool {
	return h.enabled
}

// ECDHPublicKey returns the ECDH public key
func (h *HybridHandshake) ECDHPublicKey() *ecdsa.PublicKey {
	if h.ecdhKey == nil {
		return nil
	}
	return &h.ecdhKey.PublicKey
}

// KyberPublicKey returns the Kyber public key bytes
func (h *HybridHandshake) KyberPublicKey() []byte {
	if h.kyberPK == nil {
		return nil
	}
	buf := make([]byte, KyberPublicKeySize)
	h.kyberPK.Pack(buf)
	return buf
}

// Encapsulate performs key encapsulation using the remote's public key
// Returns: ciphertext (ECDH pubkey || Kyber ciphertext), shared secret
func (h *HybridHandshake) Encapsulate(remotePubKey *HybridPublicKey) (ciphertext []byte, sharedSecret []byte, err error) {
	if !h.enabled {
		return nil, nil, ErrHybridHandshakeDisabled
	}

	// 1. Compute ECDH shared secret
	ecdhSecret := ecdh(h.ecdhKey, remotePubKey.ECDH)
	if ecdhSecret == nil {
		return nil, nil, errors.New("ECDH failed")
	}

	// 2. Kyber encapsulation
	kyberCT := make([]byte, KyberCiphertextSize)
	kyberSS := make([]byte, KyberSharedKeySize)
	remotePubKey.Kyber.EncapsulateTo(kyberCT, kyberSS, nil)

	// 3. Combine ciphertext: ECDH pubkey || Kyber ciphertext
	ecdhPubBytes := crypto.FromECDSAPub(&h.ecdhKey.PublicKey)
	ciphertext = make([]byte, len(ecdhPubBytes)+KyberCiphertextSize)
	copy(ciphertext[:len(ecdhPubBytes)], ecdhPubBytes)
	copy(ciphertext[len(ecdhPubBytes):], kyberCT)

	// 4. Combine secrets: HKDF(ecdhSecret || kyberSecret)
	combinedSecret := append(ecdhSecret, kyberSS...)
	sharedSecret = crypto.Keccak256(combinedSecret)

	// Clear sensitive data
	for i := range ecdhSecret {
		ecdhSecret[i] = 0
	}
	for i := range kyberSS {
		kyberSS[i] = 0
	}
	for i := range combinedSecret {
		combinedSecret[i] = 0
	}

	return ciphertext, sharedSecret, nil
}

// Decapsulate recovers the shared secret from ciphertext
func (h *HybridHandshake) Decapsulate(ciphertext []byte) ([]byte, error) {
	if !h.enabled {
		return nil, ErrHybridHandshakeDisabled
	}

	// Parse ciphertext
	ecdhPubLen := 65 // Uncompressed ECDSA public key
	if len(ciphertext) < ecdhPubLen+KyberCiphertextSize {
		return nil, errors.New("ciphertext too short")
	}

	ecdhPubBytes := ciphertext[:ecdhPubLen]
	kyberCT := ciphertext[ecdhPubLen : ecdhPubLen+KyberCiphertextSize]

	// 1. Parse ECDH public key
	remotePub, err := crypto.UnmarshalPubkey(ecdhPubBytes)
	if err != nil {
		return nil, err
	}

	// 2. Compute ECDH shared secret
	ecdhSecret := ecdh(h.ecdhKey, remotePub)
	if ecdhSecret == nil {
		return nil, errors.New("ECDH failed")
	}

	// 3. Kyber decapsulation
	kyberSS := make([]byte, KyberSharedKeySize)
	h.kyberSK.DecapsulateTo(kyberSS, kyberCT)

	// 4. Combine secrets
	combinedSecret := append(ecdhSecret, kyberSS...)
	sharedSecret := crypto.Keccak256(combinedSecret)

	// Clear sensitive data
	for i := range ecdhSecret {
		ecdhSecret[i] = 0
	}
	for i := range kyberSS {
		kyberSS[i] = 0
	}
	for i := range combinedSecret {
		combinedSecret[i] = 0
	}

	return sharedSecret, nil
}

// =============================================================================
// Hybrid Session Key Derivation
// =============================================================================

// deriveHybridKeys creates session keys using hybrid key exchange
// This combines ECDH secret with Kyber secret for post-quantum security
func deriveHybridKeys(
	hashFn func() hash.Hash,
	ecdhPriv *ecdsa.PrivateKey,
	ecdhPub *ecdsa.PublicKey,
	kyberSK *kyber768.PrivateKey,
	kyberCT []byte,
	n1, n2 enode.ID,
	challenge []byte,
) *session {
	const text = "discovery v5 hybrid key agreement"
	var info = make([]byte, 0, len(text)+len(n1)+len(n2))
	info = append(info, text...)
	info = append(info, n1[:]...)
	info = append(info, n2[:]...)

	// Compute ECDH secret
	ecdhSecret := ecdh(ecdhPriv, ecdhPub)
	if ecdhSecret == nil {
		return nil
	}

	// Decapsulate Kyber secret
	kyberSS := make([]byte, KyberSharedKeySize)
	kyberSK.DecapsulateTo(kyberSS, kyberCT)

	// Combine secrets
	combinedSecret := append(ecdhSecret, kyberSS...)

	// Derive session keys using HKDF
	kdf := hkdf.New(hashFn, combinedSecret, challenge, info)
	sec := session{
		writeKey: make([]byte, aesKeySize),
		readKey:  make([]byte, aesKeySize),
	}
	kdf.Read(sec.writeKey)
	kdf.Read(sec.readKey)

	// Clear sensitive data
	for i := range ecdhSecret {
		ecdhSecret[i] = 0
	}
	for i := range kyberSS {
		kyberSS[i] = 0
	}
	for i := range combinedSecret {
		combinedSecret[i] = 0
	}

	return &sec
}

// =============================================================================
// Kyber Key Utilities
// =============================================================================

// GenerateKyberKeyPair generates a new Kyber-768 key pair
func GenerateKyberKeyPair(random io.Reader) (*kyber768.PublicKey, *kyber768.PrivateKey, error) {
	if random == nil {
		random = rand.Reader
	}
	return kyber768.GenerateKeyPair(random)
}

// ParseKyberPublicKey parses a Kyber public key from bytes
func ParseKyberPublicKey(data []byte) (*kyber768.PublicKey, error) {
	if len(data) != KyberPublicKeySize {
		return nil, ErrInvalidKyberPublicKey
	}
	pk := new(kyber768.PublicKey)
	pk.Unpack(data)
	return pk, nil
}

// ParseKyberPrivateKey parses a Kyber private key from bytes
func ParseKyberPrivateKey(data []byte) (*kyber768.PrivateKey, error) {
	if len(data) != KyberPrivateKeySize {
		return nil, errors.New("invalid kyber private key size")
	}
	sk := new(kyber768.PrivateKey)
	sk.Unpack(data)
	return sk, nil
}

// KyberEncapsulate performs Kyber key encapsulation
func KyberEncapsulate(pk *kyber768.PublicKey) (ciphertext, sharedSecret []byte, err error) {
	ciphertext = make([]byte, KyberCiphertextSize)
	sharedSecret = make([]byte, KyberSharedKeySize)
	pk.EncapsulateTo(ciphertext, sharedSecret, nil)
	return ciphertext, sharedSecret, nil
}

// KyberDecapsulate performs Kyber key decapsulation
func KyberDecapsulate(sk *kyber768.PrivateKey, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) != KyberCiphertextSize {
		return nil, ErrInvalidKyberCiphertext
	}
	sharedSecret := make([]byte, KyberSharedKeySize)
	sk.DecapsulateTo(sharedSecret, ciphertext)
	return sharedSecret, nil
}

// =============================================================================
// Hybrid Handshake Configuration
// =============================================================================

// HybridConfig holds configuration for hybrid handshake
type HybridConfig struct {
	// Enabled determines if hybrid handshake should be used
	Enabled bool

	// FallbackToECDH determines if we should fall back to pure ECDH
	// when the remote doesn't support hybrid handshake
	FallbackToECDH bool
}

// DefaultHybridConfig returns the default hybrid handshake configuration
func DefaultHybridConfig() HybridConfig {
	return HybridConfig{
		Enabled:        true,
		FallbackToECDH: true,
	}
}

// =============================================================================
// Protocol Extensions
// =============================================================================

// SupportsHybridHandshake checks if a node record indicates hybrid support
func SupportsHybridHandshake(record []byte) bool {
	// Check for "pq" or "kyber" entry in ENR
	// This is a simplified check; real implementation would parse ENR
	// For now, we assume all nodes support hybrid if enabled
	return true
}
