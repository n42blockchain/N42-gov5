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

package encrypted

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"
)

// cryptoRandRead is the random reader used by this package.
// It is a package-level variable to allow deterministic testing.
var cryptoRandRead = func(b []byte) (int, error) {
	return io.ReadFull(rand.Reader, b)
}

var (
	// ErrEpochNotFound is returned when the requested epoch key does not exist.
	ErrEpochNotFound = errors.New("keyper: epoch key not found")

	// ErrNoCurrentKey is returned when no epoch key has been generated yet.
	ErrNoCurrentKey = errors.New("keyper: no current key available")
)

// Keyper manages threshold key pairs across epochs. Each epoch corresponds
// to a range of block numbers and has its own key pair for encrypting and
// decrypting transactions.
type Keyper interface {
	// GenerateEpochKey generates a new threshold key pair for an epoch.
	// Returns the public key that transaction submitters use for encryption.
	GenerateEpochKey(epoch uint64) (publicKey []byte, err error)

	// GetPublicKey returns the current (latest) threshold public key.
	GetPublicKey() ([]byte, error)

	// GetPrivateShare returns this node's private key share for the given epoch.
	GetPrivateShare(epoch uint64) ([]byte, error)
}

// keyPair holds a single epoch's key material.
type keyPair struct {
	publicKey    []byte
	privateShare []byte
}

// SimpleKeyper is a simplified keyper that generates and stores
// AES-256 key pairs per epoch. In production this would coordinate
// with other nodes via a DKG protocol; here each node generates
// independent keys for development and testing.
type SimpleKeyper struct {
	mu           sync.RWMutex
	keys         map[uint64]keyPair
	latestEpoch  uint64
	hasLatest    bool
}

// NewSimpleKeyper creates a new SimpleKeyper with an empty key store.
func NewSimpleKeyper() *SimpleKeyper {
	return &SimpleKeyper{
		keys: make(map[uint64]keyPair),
	}
}

// GenerateEpochKey generates a new AES-256 key pair for the given epoch.
// Both the "public key" and "private share" are 32-byte random values.
//
// In a real threshold system, the public key would be a point on an
// elliptic curve and the private share would be a Shamir secret share.
// Here we use symmetric keys for simplicity: the public key serves as
// an identifier and the private share is used to derive the KEK.
func (sk *SimpleKeyper) GenerateEpochKey(epoch uint64) ([]byte, error) {
	sk.mu.Lock()
	defer sk.mu.Unlock()

	// If the epoch already has a key, return the existing public key.
	if kp, exists := sk.keys[epoch]; exists {
		out := make([]byte, len(kp.publicKey))
		copy(out, kp.publicKey)
		return out, nil
	}

	publicKey := make([]byte, aesKeySize)
	if _, err := cryptoRandRead(publicKey); err != nil {
		return nil, fmt.Errorf("keyper: failed to generate public key: %w", err)
	}

	privateShare := make([]byte, aesKeySize)
	if _, err := cryptoRandRead(privateShare); err != nil {
		return nil, fmt.Errorf("keyper: failed to generate private share: %w", err)
	}

	sk.keys[epoch] = keyPair{
		publicKey:    publicKey,
		privateShare: privateShare,
	}
	if !sk.hasLatest || epoch > sk.latestEpoch {
		sk.latestEpoch = epoch
		sk.hasLatest = true
	}

	out := make([]byte, len(publicKey))
	copy(out, publicKey)
	return out, nil
}

// GetPublicKey returns the public key of the latest epoch.
// Returns ErrNoCurrentKey if no epoch key has been generated.
func (sk *SimpleKeyper) GetPublicKey() ([]byte, error) {
	sk.mu.RLock()
	defer sk.mu.RUnlock()

	if !sk.hasLatest {
		return nil, ErrNoCurrentKey
	}

	kp, exists := sk.keys[sk.latestEpoch]
	if !exists {
		return nil, ErrNoCurrentKey
	}

	out := make([]byte, len(kp.publicKey))
	copy(out, kp.publicKey)
	return out, nil
}

// GetPrivateShare returns the private key share for the given epoch.
// Returns ErrEpochNotFound if the epoch key does not exist.
func (sk *SimpleKeyper) GetPrivateShare(epoch uint64) ([]byte, error) {
	sk.mu.RLock()
	defer sk.mu.RUnlock()

	kp, exists := sk.keys[epoch]
	if !exists {
		return nil, fmt.Errorf("%w: epoch %d", ErrEpochNotFound, epoch)
	}

	out := make([]byte, len(kp.privateShare))
	copy(out, kp.privateShare)
	return out, nil
}

// PurgeEpoch removes the key material for a given epoch. This should be
// called after the epoch's transactions have all been decrypted and the
// key material is no longer needed, to limit the window of exposure.
func (sk *SimpleKeyper) PurgeEpoch(epoch uint64) {
	sk.mu.Lock()
	defer sk.mu.Unlock()

	if kp, exists := sk.keys[epoch]; exists {
		for i := range kp.privateShare {
			kp.privateShare[i] = 0
		}
		for i := range kp.publicKey {
			kp.publicKey[i] = 0
		}
		delete(sk.keys, epoch)
	}
}

// EpochCount returns the number of epochs with stored key material.
func (sk *SimpleKeyper) EpochCount() int {
	sk.mu.RLock()
	defer sk.mu.RUnlock()
	return len(sk.keys)
}
