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
// This is a proposed EIP for counting leading zeros in a 256-bit value
// =============================================================================

// enable7939 applies EIP-7939 "CLZ - Count Leading Zeros"
// - Adds CLZ (0x1e) - count leading zeros
func enable7939(jt *JumpTable) {
	jt[CLZ] = &operation{
		execute:     opClz,
		constantGas: GasFastStep,
		numPop:      1,
		numPush:     1,
	}
}

// opClz implements CLZ (0x1e) - Count Leading Zeros
// Returns the number of leading zero bits in a 256-bit value
func opClz(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	x := scope.Stack.Peek()
	
	// Count leading zeros in 256-bit value
	// uint256 is stored as [4]uint64 in little-endian order
	// We need to check from most significant to least significant
	var result uint64
	
	if x.IsZero() {
		result = 256
	} else {
		// Get the bytes and count leading zeros
		bytes := x.Bytes32()
		result = 0
		for i := 0; i < 32; i++ {
			if bytes[i] == 0 {
				result += 8
			} else {
				result += uint64(bits.LeadingZeros8(bytes[i]))
				break
			}
		}
	}
	
	x.SetUint64(result)
	return nil, nil
}

// =============================================================================
// Alternative CLZ implementation using uint256 internal structure
// =============================================================================

// opClzFast is an optimized CLZ implementation using uint256 internals
func opClzFast(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	x := scope.Stack.Peek()
	
	// uint256.Int is [4]uint64 in little-endian order
	// x[3] is the most significant limb
	arr := x.IsZero()
	if arr {
		x.SetUint64(256)
		return nil, nil
	}
	
	// Calculate CLZ using bits.LeadingZeros64
	// We need to find the first non-zero limb from the top
	var clz uint64
	bytes32 := x.Bytes32()
	
	// bytes32[0:8] = limb[3] (most significant)
	// bytes32[8:16] = limb[2]
	// bytes32[16:24] = limb[1]
	// bytes32[24:32] = limb[0] (least significant)
	
	limb3 := uint64(bytes32[0])<<56 | uint64(bytes32[1])<<48 | uint64(bytes32[2])<<40 | uint64(bytes32[3])<<32 |
		uint64(bytes32[4])<<24 | uint64(bytes32[5])<<16 | uint64(bytes32[6])<<8 | uint64(bytes32[7])
	limb2 := uint64(bytes32[8])<<56 | uint64(bytes32[9])<<48 | uint64(bytes32[10])<<40 | uint64(bytes32[11])<<32 |
		uint64(bytes32[12])<<24 | uint64(bytes32[13])<<16 | uint64(bytes32[14])<<8 | uint64(bytes32[15])
	limb1 := uint64(bytes32[16])<<56 | uint64(bytes32[17])<<48 | uint64(bytes32[18])<<40 | uint64(bytes32[19])<<32 |
		uint64(bytes32[20])<<24 | uint64(bytes32[21])<<16 | uint64(bytes32[22])<<8 | uint64(bytes32[23])
	limb0 := uint64(bytes32[24])<<56 | uint64(bytes32[25])<<48 | uint64(bytes32[26])<<40 | uint64(bytes32[27])<<32 |
		uint64(bytes32[28])<<24 | uint64(bytes32[29])<<16 | uint64(bytes32[30])<<8 | uint64(bytes32[31])

	if limb3 != 0 {
		clz = uint64(bits.LeadingZeros64(limb3))
	} else if limb2 != 0 {
		clz = 64 + uint64(bits.LeadingZeros64(limb2))
	} else if limb1 != 0 {
		clz = 128 + uint64(bits.LeadingZeros64(limb1))
	} else {
		clz = 192 + uint64(bits.LeadingZeros64(limb0))
	}
	
	x.SetUint64(clz)
	return nil, nil
}

// =============================================================================
// CTZ - Count Trailing Zeros (for completeness, not in current EIPs)
// =============================================================================

// opCtz implements CTZ - Count Trailing Zeros
func opCtz(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	x := scope.Stack.Peek()
	
	if x.IsZero() {
		x.SetUint64(256)
		return nil, nil
	}
	
	bytes32 := x.Bytes32()
	var ctz uint64
	
	// Count from least significant byte
	for i := 31; i >= 0; i-- {
		if bytes32[i] == 0 {
			ctz += 8
		} else {
			ctz += uint64(bits.TrailingZeros8(bytes32[i]))
			break
		}
	}
	
	x.SetUint64(ctz)
	return nil, nil
}

// =============================================================================
// POPCOUNT - Population Count (for completeness, not in current EIPs)
// =============================================================================

// opPopcount implements POPCOUNT - count set bits
func opPopcount(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	x := scope.Stack.Peek()
	
	bytes32 := x.Bytes32()
	var count uint64
	for _, b := range bytes32 {
		count += uint64(bits.OnesCount8(b))
	}
	
	x.SetUint64(count)
	return nil, nil
}

func init() {
	// Register Prague EIPs
	activators[7939] = enable7939
}

