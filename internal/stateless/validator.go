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

// Package stateless provides stateless block validation using witnesses.
// A stateless validator verifies blocks using only the block data and an
// accompanying witness (Merkle proofs), without requiring access to the
// full state database.
package stateless

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state/witness"
	"github.com/n42blockchain/N42/params"
)

var (
	// ErrNilBlock is returned when a nil block is passed to ValidateBlock.
	ErrNilBlock = errors.New("stateless: nil block")

	// ErrNilWitness is returned when a nil witness is passed to ValidateBlock.
	ErrNilWitness = errors.New("stateless: nil witness")

	// ErrWitnessRootMismatch is returned when the witness parent root does
	// not match the block's expected parent state root.
	ErrWitnessRootMismatch = errors.New("stateless: witness parent root mismatch")

	// ErrProofVerificationFailed is returned when Merkle proof verification fails.
	ErrProofVerificationFailed = errors.New("stateless: proof verification failed")
)

// Validator verifies blocks using only a witness (no state DB).
// This is the minimal viable stateless validation: it checks that the
// witness Merkle proofs are cryptographically valid against the declared
// parent state root. Full re-execution can be added later.
type Validator struct {
	chainConfig *params.ChainConfig
}

// NewValidator creates a new stateless block Validator.
func NewValidator(config *params.ChainConfig) *Validator {
	return &Validator{
		chainConfig: config,
	}
}

// ValidateBlock verifies a block using the accompanying witness.
// It checks that all Merkle proofs in the witness are valid against
// the witness's parent state root.
//
// This is the minimal viable stateless validation -- proof verification only.
// Full transaction re-execution against the witness state can be added later.
//
// Returns nil if the block is valid.
func (v *Validator) ValidateBlock(blk block.IBlock, w *witness.BlockWitness) error {
	if blk == nil {
		return ErrNilBlock
	}
	if w == nil {
		return ErrNilWitness
	}

	// Verify that the witness's parent root is non-zero.
	// A zero root would mean the witness was not properly populated.
	if w.ParentRoot == (types.Hash{}) {
		return fmt.Errorf("%w: witness has zero parent root", ErrWitnessRootMismatch)
	}

	// Verify all Merkle proofs in the witness.
	if err := witness.VerifyWitness(w.ParentRoot, w); err != nil {
		return fmt.Errorf("%w: %v", ErrProofVerificationFailed, err)
	}

	return nil
}

// ChainConfig returns the chain configuration used by this validator.
func (v *Validator) ChainConfig() *params.ChainConfig {
	return v.chainConfig
}
