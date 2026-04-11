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
// EVM gas accounting primitives. Exports the canonical gas-tier
// constants (GasQuickStep, GasFastestStep, GasFastStep, GasMidStep,
// GasSlowStep, GasExtStep) shared by every opcode's constantGas field.
// Provides safeMul/safeAdd uint64 helpers that report overflow without
// panicking, toWordSize for byte-to-word rounding, and callGas which
// implements the EIP-150 63/64 rule so callers cannot re-forward more
// gas than they hold.

package vm

import (
	"github.com/holiman/uint256"
)

// Gas costs
const (
	GasQuickStep   uint64 = 2
	GasFastestStep uint64 = 3
	GasFastStep    uint64 = 5
	GasMidStep     uint64 = 8
	GasSlowStep    uint64 = 10
	GasExtStep     uint64 = 20
)

// safeMul multiplies two uint64 values and returns the result and a boolean
// indicating whether overflow occurred.
func safeMul(a, b uint64) (uint64, bool) {
	if a == 0 || b == 0 {
		return 0, false
	}
	c := a * b
	return c, c/a != b
}

// safeAdd adds two uint64 values and returns the result and a boolean
// indicating whether overflow occurred.
func safeAdd(a, b uint64) (uint64, bool) {
	c := a + b
	return c, c < a
}

// toWordSize is a package-internal alias for ToWordSize.
// It converts a size in bytes to the number of 32-byte words, rounding up.
func toWordSize(size uint64) uint64 {
	return ToWordSize(size)
}

// callGas returns the actual gas cost of the call.
//
// The cost of gas was changed during the homestead price change HF.
// As part of EIP 150 (TangerineWhistle), the returned gas is gas - base * 63 / 64.
func callGas(isEip150 bool, availableGas, base uint64, callCost *uint256.Int) (uint64, error) {
	if isEip150 {
		availableGas = availableGas - base
		gas := availableGas - availableGas/64
		// If the bit length exceeds 64 bit we know that the newly calculated "gas" for EIP150
		// is smaller than the requested amount. Therefore we return the new gas instead
		// of returning an error.
		if !callCost.IsUint64() || gas < callCost.Uint64() {
			return gas, nil
		}
	}
	if !callCost.IsUint64() {
		return 0, ErrGasUintOverflow
	}

	return callCost.Uint64(), nil
}
