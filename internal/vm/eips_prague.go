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

package vm

import (
	"math/bits"
)

// =============================================================================
// EIP-7939: CLZ - Count Leading Zeros (Prague/Fusaka)
// https://eips.ethereum.org/EIPS/eip-7939
// =============================================================================

// enable7939 applies EIP-7939 "CLZ - Count Leading Zeros"
// - Adds CLZ (0x1e) - count leading zeros in a 256-bit value
func enable7939(jt *JumpTable) {
	jt[CLZ] = &operation{
		execute:     opClz,
		constantGas: GasFastStep,
		numPop:      1,
		numPush:     1,
	}
}

// opClz implements CLZ (0x1e) - Count Leading Zeros
// Returns the number of leading zero bits in a 256-bit value.
// For a zero input, returns 256.
func opClz(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	x := scope.Stack.Peek()

	if x.IsZero() {
		x.SetUint64(256)
		return nil, nil
	}

	b32 := x.Bytes32()
	var result uint64
	for i := 0; i < 32; i++ {
		if b32[i] == 0 {
			result += 8
		} else {
			result += uint64(bits.LeadingZeros8(b32[i]))
			break
		}
	}

	x.SetUint64(result)
	return nil, nil
}

func init() {
	activators[7939] = enable7939
}
