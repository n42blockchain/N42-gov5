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
)

var (
	// ErrParentRootMismatch is returned when the witness parent root does not
	// match the expected parent root from the block header.
	ErrParentRootMismatch = errors.New("witness: parent root mismatch")

	// ErrStatelessExecutionNotImplemented indicates that full stateless EVM
	// execution is not yet wired. Proof verification and state reader
	// construction are functional; transaction re-execution requires
	// integration with the EVM, consensus engine, and block processor.
	ErrStatelessExecutionNotImplemented = errors.New("witness: stateless EVM execution not yet implemented")
)

// VerifyBlockStateless performs stateless block verification using a witness.
//
// The verification process:
//  1. Verify that the witness parent root matches the expected parent root.
//  2. Verify all Merkle proofs in the witness against the parent root.
//  3. Construct a WitnessStateReader from the verified witness data.
//  4. (Future) Re-execute all transactions using the WitnessStateReader.
//  5. (Future) Return the resulting state root for comparison with the block header.
//
// Currently, steps 1-3 are fully implemented. Step 4-5 require integration
// with the EVM execution pipeline and are marked as TODO.
//
// Parameters:
//   - parentRoot: the expected state root before this block's execution
//   - witness: the block witness containing all required Merkle proofs
//   - txs: raw encoded transactions for re-execution (reserved for future use)
//
// Returns the verified parent root (future: post-execution state root) and any error.
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
	_, err := NewWitnessStateReader(witness)
	if err != nil {
		return types.Hash{}, fmt.Errorf("failed to create witness state reader: %w", err)
	}

	// Step 4-5: Re-execute transactions and compute post-state root.
	// TODO(light-client): Wire EVM execution using the WitnessStateReader:
	//
	//   ibs := state.New(stateReader)
	//   for _, rawTx := range txs {
	//       tx := decodeTx(rawTx)
	//       receipt, err := applyTransaction(chainConfig, header, ibs, tx)
	//       ...
	//   }
	//   postRoot := ibs.ComputeRoot()
	//   return postRoot, nil
	//
	// For now, return the parent root to indicate successful proof verification.
	// The caller can use VerifyWitness directly for proof-only verification.
	return parentRoot, nil
}
