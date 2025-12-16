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

package misc

import (
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

// Inturn defines the interface for checking if a signer is in-turn.
type Inturn interface {
	// Inturn returns true if the signer is in-turn for the given block number.
	Inturn(blockNumber uint64, signer types.Address) bool
}

// CalcDifficulty returns the difficulty that a new block should have:
//   - DiffInTurn (2) if the signer is in-turn
//   - DiffNoTurn (1) if the signer is out-of-turn
func CalcDifficulty(inturn Inturn, blockNumber uint64, signer types.Address) *uint256.Int {
	if inturn.Inturn(blockNumber, signer) {
		return new(uint256.Int).Set(DiffInTurn)
	}
	return new(uint256.Int).Set(DiffNoTurn)
}

// ValidateDifficulty checks if the difficulty is valid for PoA consensus.
// Difficulty must be either 1 (out-of-turn) or 2 (in-turn).
func ValidateDifficulty(difficulty *uint256.Int) error {
	if difficulty.IsZero() {
		return ErrInvalidDifficulty
	}
	if difficulty.Cmp(DiffInTurn) != 0 && difficulty.Cmp(DiffNoTurn) != 0 {
		return ErrInvalidDifficulty
	}
	return nil
}

// VerifyDifficulty checks if the difficulty matches the expected value based on turn.
func VerifyDifficulty(difficulty *uint256.Int, inturn bool) error {
	if inturn && difficulty.Cmp(DiffInTurn) != 0 {
		return ErrWrongDifficulty
	}
	if !inturn && difficulty.Cmp(DiffNoTurn) != 0 {
		return ErrWrongDifficulty
	}
	return nil
}

