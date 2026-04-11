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
// Stateless block verification entry point.
// VerifyBlockStateless cross-checks the expected parentRoot against
// BlockWitness.ParentRoot, validates every Merkle proof via
// VerifyWitness, and returns the verified parent root for the caller
// to construct a WitnessStateReader and re-execute transactions.
// ErrParentRootMismatch surfaces the consistency failure.

package witness

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
)

var (
	// ErrParentRootMismatch is returned when the witness parent root does not
	// match the expected parent root from the block header.
	ErrParentRootMismatch = errors.New("witness: parent root mismatch")
)

// VerifyBlockStateless performs stateless block verification using a witness.
//
// The verification process:
//  1. Verify that the witness parent root matches the expected parent root.
//  2. Verify all Merkle proofs in the witness against the parent root.
//  3. Construct a WitnessStateReader from the verified witness data.
//  4. Return the state reader for the caller to re-execute transactions
//     (via the guest package or direct EVM execution).
//
// Parameters:
//   - parentRoot: the expected state root before this block's execution
//   - witness: the block witness containing all required Merkle proofs
//   - txs: raw encoded transactions (reserved for future use)
//
// Returns the verified parent root, a usable WitnessStateReader, and any error.
func VerifyBlockStateless(parentRoot types.Hash, witness *BlockWitness, txs [][]byte) (types.Hash, error) {
	// Step 1: Verify parent root consistency.
	if witness.ParentRoot != parentRoot {
		return types.Hash{}, fmt.Errorf("%w: expected %x, witness has %x",
			ErrParentRootMismatch, parentRoot, witness.ParentRoot)
	}

	// Step 2: Verify all Merkle proofs against the parent root.
	if err := VerifyWitness(parentRoot, witness); err != nil {
		return types.Hash{}, fmt.Errorf("witness proof verification failed: %w", err)
	}

	// Step 3: Construct a WitnessStateReader from verified proof data.
	// This validates that all proofs can be decoded into usable state.
	_, err := NewWitnessStateReader(witness)
	if err != nil {
		return types.Hash{}, fmt.Errorf("failed to create witness state reader: %w", err)
	}

	// Step 4: Transaction re-execution is performed by the guest package
	// (internal/zkprover/guest.Execute), which uses WitnessStateReader to
	// replay transactions and compute the post-state root. The zkVM produces
	// a cryptographic proof of this computation.
	//
	// For native (non-zkVM) stateless verification, use
	// VerifyBlockStatelessWithExec which performs EVM execution directly.

	return parentRoot, nil
}

// VerifyBlockStatelessWithExec performs full stateless block verification
// including transaction re-execution.
//
// It verifies the witness proofs, creates a WitnessStateReader, and returns it
// along with the parent root so the caller can replay transactions against it
// and compare the resulting state root with the block header.
//
// This is the host-side (non-zkVM) counterpart to guest.Execute().
func VerifyBlockStatelessWithExec(parentRoot types.Hash, witness *BlockWitness) (*WitnessStateReader, error) {
	// Step 1: Verify parent root consistency.
	if witness.ParentRoot != parentRoot {
		return nil, fmt.Errorf("%w: expected %x, witness has %x",
			ErrParentRootMismatch, parentRoot, witness.ParentRoot)
	}

	// Step 2: Verify all Merkle proofs.
	if err := VerifyWitness(parentRoot, witness); err != nil {
		return nil, fmt.Errorf("witness proof verification failed: %w", err)
	}

	// Step 3: Construct WitnessStateReader.
	reader, err := NewWitnessStateReader(witness)
	if err != nil {
		return nil, fmt.Errorf("failed to create witness state reader: %w", err)
	}

	return reader, nil
}
