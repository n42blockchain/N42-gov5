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
//
// Transaction decryptor for the threshold-encrypted mempool. Wraps an
// AES-CTR stream cipher with HMAC-SHA256 authentication, deriving
// per-transaction keys from the keyper-released epoch secret. Used at
// block assembly time to unseal enqueued ciphertexts in deterministic
// order.

package encrypted

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"

	"go.uber.org/zap"
)

var (
	// ErrInvalidKeyShare is returned when the private key share is invalid.
	ErrInvalidKeyShare = errors.New("threshold decryptor: invalid private key share")

	// ErrInvalidPublicKey is returned when the public key is invalid.
	ErrInvalidPublicKey = errors.New("threshold decryptor: invalid public key")

	// ErrInvalidThreshold is returned when threshold parameters are invalid.
	ErrInvalidThreshold = errors.New("threshold decryptor: threshold must be > 0 and <= total shares")

	// ErrCiphertextTooShort is returned when the ciphertext is shorter than
	// the required nonce + tag overhead.
	ErrCiphertextTooShort = errors.New("threshold decryptor: ciphertext too short")

	// ErrKeyDecryptionFailed is returned when the per-tx symmetric key
	// cannot be decrypted from the encrypted key envelope.
	ErrKeyDecryptionFailed = errors.New("threshold decryptor: key decryption failed")

	// aesKeySize is the expected AES-256 key length in bytes.
	aesKeySize = 32
)

// ThresholdDecryptor implements the Decryptor interface using a simplified
// threshold decryption scheme. In production this would use a full DKG
// (Distributed Key Generation) protocol; the current implementation derives
// a symmetric decryption key from the node's private key share to enable
// local decryption of per-transaction AES-256-GCM ciphertexts.
type ThresholdDecryptor struct {
	privateKeyShare []byte // this node's private key share
	publicKey       []byte // combined threshold public key
	kek             []byte // cached key-encryption-key (derived once at construction)
	threshold       int    // minimum shares needed for reconstruction
	totalShares     int    // total number of key shares
	closed          bool
	mu              sync.Mutex
	logger          *zap.Logger
}

// NewThresholdDecryptor creates a new threshold decryptor.
//
// privateShare is this node's share of the threshold private key.
// publicKey is the combined threshold public key used for encryption.
// threshold is the minimum number of shares needed to decrypt.
// total is the total number of key shares distributed.
func NewThresholdDecryptor(privateShare, publicKey []byte, threshold, total int, logger *zap.Logger) (*ThresholdDecryptor, error) {
	if len(privateShare) == 0 {
		return nil, ErrInvalidKeyShare
	}
	if len(publicKey) == 0 {
		return nil, ErrInvalidPublicKey
	}
	if threshold <= 0 || threshold > total {
		return nil, ErrInvalidThreshold
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	privCopy := make([]byte, len(privateShare))
	copy(privCopy, privateShare)

	pubCopy := make([]byte, len(publicKey))
	copy(pubCopy, publicKey)

	td := &ThresholdDecryptor{
		privateKeyShare: privCopy,
		publicKey:       pubCopy,
		threshold:       threshold,
		totalShares:     total,
		logger:          logger,
	}
	// Derive KEK once at construction time using HKDF.
	td.kek = deriveKEK(privCopy, pubCopy)

	return td, nil
}

// Decrypt decrypts an encrypted transaction payload.
//
// It first decrypts the per-transaction symmetric key from encryptionKey
// using the threshold key share, then uses that symmetric key to decrypt
// encryptedData via AES-256-GCM.
func (td *ThresholdDecryptor) Decrypt(encryptedData []byte, encryptionKey []byte) ([]byte, error) {
	// Step 1: Decrypt the per-tx symmetric key.
	symmetricKey, err := td.DecryptKey(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecryptionFailed, err)
	}

	// Step 2: Use the symmetric key to decrypt the transaction data.
	plaintext, err := decryptAESGCM(symmetricKey, encryptedData)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecryptionFailed, err)
	}

	return plaintext, nil
}

// PublicKey returns the threshold public key.
func (td *ThresholdDecryptor) PublicKey() []byte {
	out := make([]byte, len(td.publicKey))
	copy(out, td.publicKey)
	return out
}

// DecryptKey decrypts the per-transaction symmetric key using this node's
// private key share.
//
// In this simplified implementation the encrypted key is itself an
// AES-256-GCM ciphertext produced with a key derived from the threshold
// public key via HKDF. A full production system would use
// Shamir/Feldman VSS and combine partial decryptions from t-of-n nodes.
func (td *ThresholdDecryptor) DecryptKey(encryptedKey []byte) ([]byte, error) {
	if len(encryptedKey) == 0 {
		return nil, ErrKeyDecryptionFailed
	}

	td.mu.Lock()
	if td.closed {
		td.mu.Unlock()
		return nil, errors.New("threshold decryptor: closed")
	}
	kek := make([]byte, len(td.kek))
	copy(kek, td.kek)
	td.mu.Unlock()

	plainKey, err := decryptAESGCM(kek, encryptedKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKeyDecryptionFailed, err)
	}

	if len(plainKey) != aesKeySize {
		return nil, fmt.Errorf("%w: unexpected key length %d", ErrKeyDecryptionFailed, len(plainKey))
	}
	return plainKey, nil
}

// Close zeros all key material held by the decryptor. After Close is called,
// Decrypt and DecryptKey will return an error.
func (td *ThresholdDecryptor) Close() {
	td.mu.Lock()
	defer td.mu.Unlock()

	for i := range td.privateKeyShare {
		td.privateKeyShare[i] = 0
	}
	for i := range td.publicKey {
		td.publicKey[i] = 0
	}
	for i := range td.kek {
		td.kek[i] = 0
	}
	td.closed = true
}

// deriveKEK derives the key-encryption-key from the private key share
// and public key using HKDF (HMAC-based Extract-and-Expand Key Derivation Function).
func deriveKEK(privateShare, publicKey []byte) []byte {
	// HKDF-Extract: PRK = HMAC-SHA256(salt, IKM)
	salt := []byte("n42-threshold-kek-v1")
	mac := hmac.New(sha256.New, salt)
	mac.Write(privateShare)
	mac.Write(publicKey)
	prk := mac.Sum(nil)

	// HKDF-Expand: OKM = HMAC-SHA256(PRK, info || 0x01)
	info := []byte("encrypted-mempool-kek")
	mac2 := hmac.New(sha256.New, prk)
	mac2.Write(info)
	mac2.Write([]byte{0x01})
	return mac2.Sum(nil) // 32 bytes = AES-256
}

// decryptAESGCM decrypts ciphertext using AES-256-GCM.
// The ciphertext is expected to be: nonce || encrypted_data || tag
// where nonce length is determined by the AEAD.
func decryptAESGCM(key, ciphertext []byte) ([]byte, error) {
	if len(key) != aesKeySize {
		return nil, fmt.Errorf("invalid AES key size: got %d, want %d", len(key), aesKeySize)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cipher.NewGCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize+gcm.Overhead() {
		return nil, ErrCiphertextTooShort
	}

	nonce := ciphertext[:nonceSize]
	sealed := ciphertext[nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("gcm.Open: %w", err)
	}
	return plaintext, nil
}

// encryptAESGCM encrypts plaintext using AES-256-GCM.
// Returns: nonce || encrypted_data || tag.
// This is exported for use by transaction submitters who need to encrypt
// their transactions before submission.
func encryptAESGCM(key, plaintext []byte) ([]byte, error) {
	if len(key) != aesKeySize {
		return nil, fmt.Errorf("invalid AES key size: got %d, want %d", len(key), aesKeySize)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cipher.NewGCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	// Use crypto/rand for nonce generation.
	if _, err := cryptoRandRead(nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}
