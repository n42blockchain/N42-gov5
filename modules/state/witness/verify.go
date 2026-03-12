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

package witness

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/jmt"
)

var (
	// ErrInvalidAccountProof is returned when an account proof fails verification.
	ErrInvalidAccountProof = errors.New("witness: invalid account proof")

	// ErrInvalidStorageProof is returned when a storage proof fails verification.
	ErrInvalidStorageProof = errors.New("witness: invalid storage proof")

	// ErrProofValueMismatch is returned when the proof value does not match
	// the value claimed in the KeyProof.
	ErrProofValueMismatch = errors.New("witness: proof value mismatch")
)

// VerifyWitness verifies all Merkle proofs in a BlockWitness against the
// given parent state root. Each account and storage proof is checked for
// cryptographic validity using the JMT proof verification algorithm.
//
// Returns nil if all proofs are valid, or an error describing the first
// invalid proof encountered.
func VerifyWitness(parentRoot types.Hash, w *BlockWitness) error {
	// Convert types.Hash to jmt.Hash for proof verification.
	var root jmt.Hash
	copy(root[:], parentRoot[:])

	hasher := jmt.DefaultHasher()

	// Verify account proofs.
	for i := range w.AccountProofs {
		ap := &w.AccountProofs[i]
		if ap.KeyHash != ap.Proof.Key {
			return fmt.Errorf("%w: account proof %d: key hash mismatch: expected %x, got %x",
				ErrInvalidAccountProof, i, ap.KeyHash, ap.Proof.Key)
		}
		verifiedValue, err := jmt.VerifyProof(root, &ap.Proof, hasher)
		if err != nil {
			return fmt.Errorf("%w: account proof %d (key %x): %v",
				ErrInvalidAccountProof, i, ap.KeyHash, err)
		}
		// Verify the value matches what the KeyProof claims.
		if !bytesEqual(verifiedValue, ap.Value) {
			return fmt.Errorf("%w: account proof %d (key %x): expected %x, got %x",
				ErrProofValueMismatch, i, ap.KeyHash, ap.Value, verifiedValue)
		}
	}

	// Verify storage proofs.
	for i := range w.StorageProofs {
		sp := &w.StorageProofs[i]
		if sp.KeyHash != sp.Proof.Key {
			return fmt.Errorf("%w: storage proof %d: key hash mismatch: expected %x, got %x",
				ErrInvalidStorageProof, i, sp.KeyHash, sp.Proof.Key)
		}
		verifiedValue, err := jmt.VerifyProof(root, &sp.Proof, hasher)
		if err != nil {
			return fmt.Errorf("%w: storage proof %d (key %x): %v",
				ErrInvalidStorageProof, i, sp.KeyHash, err)
		}
		if !bytesEqual(verifiedValue, sp.Value) {
			return fmt.Errorf("%w: storage proof %d (key %x): expected %x, got %x",
				ErrProofValueMismatch, i, sp.KeyHash, sp.Value, verifiedValue)
		}
	}

	return nil
}

// bytesEqual compares two byte slices, treating nil and empty as equivalent.
func bytesEqual(a, b []byte) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
