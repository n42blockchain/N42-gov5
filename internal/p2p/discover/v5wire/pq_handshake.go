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
// Implements a hybrid key exchange combining ECDH (secp256k1) and Kyber-768
// for defense-in-depth against both classical and quantum attacks.
// If either key exchange is broken, the other still provides security.
//
// Shared secret derivation: HKDF(ecdhSecret || kyberSecret, ...)

package v5wire

import (
	"crypto/ecdsa"
	"crypto/rand"
	"errors"
	"io"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/crypto/kem/kyber/kyber768"
	"github.com/n42blockchain/N42/internal/p2p/enode"
	"golang.org/x/crypto/hkdf"
)

const (
	KyberPublicKeySize  = kyber768.PublicKeySize  // 1184 bytes
	KyberPrivateKeySize = kyber768.PrivateKeySize // 2400 bytes
	KyberCiphertextSize = kyber768.CiphertextSize // 1088 bytes
	KyberSharedKeySize  = kyber768.SharedKeySize  // 32 bytes

	// ecdhUncompressedPubLen is the byte length of an uncompressed secp256k1 public key (04 || x || y).
	ecdhUncompressedPubLen = 65
)

var (
	ErrHybridHandshakeDisabled = errors.New("hybrid PQ handshake is disabled")
	ErrKyberKeyGenFailed       = errors.New("kyber key generation failed")
	ErrKyberEncapFailed        = errors.New("kyber encapsulation failed")
	ErrKyberDecapFailed        = errors.New("kyber decapsulation failed")
	ErrInvalidKyberPublicKey   = errors.New("invalid kyber public key size")
	ErrInvalidKyberCiphertext  = errors.New("invalid kyber ciphertext size")
	ErrInvalidKyberPrivateKey  = errors.New("invalid kyber private key size")
	ErrECDHFailed              = errors.New("ECDH computation failed")
	ErrCiphertextTooShort      = errors.New("hybrid ciphertext too short")
)

// HybridPublicKey holds both ECDH and Kyber public keys for a hybrid handshake peer.
type HybridPublicKey struct {
	ECDH  *ecdsa.PublicKey
	Kyber *kyber768.PublicKey
}

// HybridPrivateKey holds both ECDH and Kyber private keys.
type HybridPrivateKey struct {
	ECDH  *ecdsa.PrivateKey
	Kyber *kyber768.PrivateKey
}

// HybridHandshake manages the state of a hybrid classical+post-quantum key exchange.
type HybridHandshake struct {
	ecdhKey *ecdsa.PrivateKey
	kyberSK *kyber768.PrivateKey
	kyberPK *kyber768.PublicKey
	enabled bool
}

// NewHybridHandshake creates a new hybrid handshake with fresh ECDH and Kyber key pairs.
// If enabled is false, returns a disabled handshake that rejects Encapsulate/Decapsulate.
func NewHybridHandshake(enabled bool) (*HybridHandshake, error) {
	if !enabled {
		return &HybridHandshake{enabled: false}, nil
	}

	ecdhKey, err := crypto.GenerateKey()
	if err != nil {
		return nil, err
	}

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

func (h *HybridHandshake) IsEnabled() bool {
	return h.enabled
}

func (h *HybridHandshake) ECDHPublicKey() *ecdsa.PublicKey {
	if h.ecdhKey == nil {
		return nil
	}
	return &h.ecdhKey.PublicKey
}

// KyberPublicKey returns the serialized Kyber public key, or nil if not initialized.
func (h *HybridHandshake) KyberPublicKey() []byte {
	if h.kyberPK == nil {
		return nil
	}
	buf := make([]byte, KyberPublicKeySize)
	h.kyberPK.Pack(buf)
	return buf
}

// Encapsulate performs hybrid key encapsulation against the remote's public key.
// The returned ciphertext is (uncompressed ECDH pubkey || Kyber ciphertext) and
// sharedSecret is Keccak256(ecdhSecret || kyberSecret).
func (h *HybridHandshake) Encapsulate(remotePubKey *HybridPublicKey) (ciphertext []byte, sharedSecret []byte, err error) {
	if !h.enabled {
		return nil, nil, ErrHybridHandshakeDisabled
	}

	ecdhSecret := ecdh(h.ecdhKey, remotePubKey.ECDH)
	if ecdhSecret == nil {
		return nil, nil, ErrECDHFailed
	}
	defer zeroBytes(ecdhSecret)

	kyberCT := make([]byte, KyberCiphertextSize)
	kyberSS := make([]byte, KyberSharedKeySize)
	remotePubKey.Kyber.EncapsulateTo(kyberCT, kyberSS, nil)
	defer zeroBytes(kyberSS)

	ecdhPubBytes := crypto.FromECDSAPub(&h.ecdhKey.PublicKey)
	ciphertext = make([]byte, ecdhUncompressedPubLen+KyberCiphertextSize)
	copy(ciphertext[:ecdhUncompressedPubLen], ecdhPubBytes)
	copy(ciphertext[ecdhUncompressedPubLen:], kyberCT)

	sharedSecret = deriveHybridSecret(ecdhSecret, kyberSS)
	return ciphertext, sharedSecret, nil
}

// Decapsulate recovers the shared secret from a hybrid ciphertext produced by Encapsulate.
func (h *HybridHandshake) Decapsulate(ciphertext []byte) ([]byte, error) {
	if !h.enabled {
		return nil, ErrHybridHandshakeDisabled
	}

	minLen := ecdhUncompressedPubLen + KyberCiphertextSize
	if len(ciphertext) < minLen {
		return nil, ErrCiphertextTooShort
	}

	remotePub, err := crypto.UnmarshalPubkey(ciphertext[:ecdhUncompressedPubLen])
	if err != nil {
		return nil, err
	}

	ecdhSecret := ecdh(h.ecdhKey, remotePub)
	if ecdhSecret == nil {
		return nil, ErrECDHFailed
	}
	defer zeroBytes(ecdhSecret)

	kyberSS := make([]byte, KyberSharedKeySize)
	h.kyberSK.DecapsulateTo(kyberSS, ciphertext[ecdhUncompressedPubLen:minLen])
	defer zeroBytes(kyberSS)

	return deriveHybridSecret(ecdhSecret, kyberSS), nil
}

// deriveHybridKeys creates session keys by combining ECDH and Kyber secrets via HKDF.
// This is the post-quantum counterpart of deriveKeys in crypto.go.
func deriveHybridKeys(
	hash hashFn,
	ecdhPriv *ecdsa.PrivateKey,
	ecdhPub *ecdsa.PublicKey,
	kyberSK *kyber768.PrivateKey,
	kyberCT []byte,
	n1, n2 enode.ID,
	challenge []byte,
) *session {
	const text = "discovery v5 hybrid key agreement"
	info := make([]byte, 0, len(text)+len(n1)+len(n2))
	info = append(info, text...)
	info = append(info, n1[:]...)
	info = append(info, n2[:]...)

	ecdhSecret := ecdh(ecdhPriv, ecdhPub)
	if ecdhSecret == nil {
		return nil
	}
	defer zeroBytes(ecdhSecret)

	kyberSS := make([]byte, KyberSharedKeySize)
	kyberSK.DecapsulateTo(kyberSS, kyberCT)
	defer zeroBytes(kyberSS)

	combined := combineSecrets(ecdhSecret, kyberSS)
	defer zeroBytes(combined)

	kdf := hkdf.New(hash, combined, challenge, info)
	sec := session{
		writeKey: make([]byte, aesKeySize),
		readKey:  make([]byte, aesKeySize),
	}
	kdf.Read(sec.writeKey)
	kdf.Read(sec.readKey)

	return &sec
}

// GenerateKyberKeyPair generates a new Kyber-768 key pair.
// If random is nil, crypto/rand.Reader is used.
func GenerateKyberKeyPair(random io.Reader) (*kyber768.PublicKey, *kyber768.PrivateKey, error) {
	if random == nil {
		random = rand.Reader
	}
	return kyber768.GenerateKeyPair(random)
}

// ParseKyberPublicKey deserializes a Kyber public key from its packed byte representation.
func ParseKyberPublicKey(data []byte) (*kyber768.PublicKey, error) {
	if len(data) != KyberPublicKeySize {
		return nil, ErrInvalidKyberPublicKey
	}
	pk := new(kyber768.PublicKey)
	pk.Unpack(data)
	return pk, nil
}

// ParseKyberPrivateKey deserializes a Kyber private key from its packed byte representation.
func ParseKyberPrivateKey(data []byte) (*kyber768.PrivateKey, error) {
	if len(data) != KyberPrivateKeySize {
		return nil, ErrInvalidKyberPrivateKey
	}
	sk := new(kyber768.PrivateKey)
	sk.Unpack(data)
	return sk, nil
}

// KyberEncapsulate performs Kyber key encapsulation against a public key.
func KyberEncapsulate(pk *kyber768.PublicKey) (ciphertext, sharedSecret []byte, err error) {
	ciphertext = make([]byte, KyberCiphertextSize)
	sharedSecret = make([]byte, KyberSharedKeySize)
	pk.EncapsulateTo(ciphertext, sharedSecret, nil)
	return ciphertext, sharedSecret, nil
}

// KyberDecapsulate recovers the shared secret from a Kyber ciphertext.
func KyberDecapsulate(sk *kyber768.PrivateKey, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) != KyberCiphertextSize {
		return nil, ErrInvalidKyberCiphertext
	}
	sharedSecret := make([]byte, KyberSharedKeySize)
	sk.DecapsulateTo(sharedSecret, ciphertext)
	return sharedSecret, nil
}

// HybridConfig holds configuration for hybrid post-quantum handshake.
type HybridConfig struct {
	Enabled        bool // Use hybrid handshake when available.
	FallbackToECDH bool // Fall back to pure ECDH when the remote lacks hybrid support.
}

// DefaultHybridConfig returns the default hybrid handshake configuration.
func DefaultHybridConfig() HybridConfig {
	return HybridConfig{
		Enabled:        true,
		FallbackToECDH: true,
	}
}

// combineSecrets concatenates two secret byte slices into a new buffer, avoiding
// append-based aliasing issues where the first slice's underlying array might be reused.
func combineSecrets(a, b []byte) []byte {
	combined := make([]byte, len(a)+len(b))
	copy(combined, a)
	copy(combined[len(a):], b)
	return combined
}

// deriveHybridSecret combines ECDH and Kyber shared secrets via Keccak256.
// The intermediate combined buffer is zeroed before returning.
func deriveHybridSecret(ecdhSecret, kyberSS []byte) []byte {
	combined := combineSecrets(ecdhSecret, kyberSS)
	defer zeroBytes(combined)
	return crypto.Keccak256(combined)
}

// zeroBytes overwrites a byte slice with zeros to erase sensitive key material.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
