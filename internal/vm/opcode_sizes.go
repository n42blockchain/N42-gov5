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

// opcodeSizes contains the total instruction size (opcode + immediate operands)
// for all opcodes. Default value is 1 for single-byte instructions.
// This lookup table replaces the large switch statement in eofOpcodeSize
// for better performance and lower cyclomatic complexity.
var opcodeSizes [256]int8

func init() {
	// Initialize all opcodes to size 1 (single byte instruction)
	for i := range opcodeSizes {
		opcodeSizes[i] = 1
	}

	// PUSH1 through PUSH32: opcode + N bytes
	opcodeSizes[PUSH1] = 2
	opcodeSizes[PUSH2] = 3
	opcodeSizes[PUSH3] = 4
	opcodeSizes[PUSH4] = 5
	opcodeSizes[PUSH5] = 6
	opcodeSizes[PUSH6] = 7
	opcodeSizes[PUSH7] = 8
	opcodeSizes[PUSH8] = 9
	opcodeSizes[PUSH9] = 10
	opcodeSizes[PUSH10] = 11
	opcodeSizes[PUSH11] = 12
	opcodeSizes[PUSH12] = 13
	opcodeSizes[PUSH13] = 14
	opcodeSizes[PUSH14] = 15
	opcodeSizes[PUSH15] = 16
	opcodeSizes[PUSH16] = 17
	opcodeSizes[PUSH17] = 18
	opcodeSizes[PUSH18] = 19
	opcodeSizes[PUSH19] = 20
	opcodeSizes[PUSH20] = 21
	opcodeSizes[PUSH21] = 22
	opcodeSizes[PUSH22] = 23
	opcodeSizes[PUSH23] = 24
	opcodeSizes[PUSH24] = 25
	opcodeSizes[PUSH25] = 26
	opcodeSizes[PUSH26] = 27
	opcodeSizes[PUSH27] = 28
	opcodeSizes[PUSH28] = 29
	opcodeSizes[PUSH29] = 30
	opcodeSizes[PUSH30] = 31
	opcodeSizes[PUSH31] = 32
	opcodeSizes[PUSH32] = 33

	// EOF-specific opcodes with immediate operands
	// RJUMP, RJUMPI: opcode + 2 byte offset
	opcodeSizes[RJUMP] = 3
	opcodeSizes[RJUMPI] = 3

	// CALLF, JUMPF: opcode + 2 byte function index
	opcodeSizes[CALLF] = 3
	opcodeSizes[JUMPF] = 3

	// DATALOADN: opcode + 2 byte offset
	opcodeSizes[DATALOADN] = 3

	// DUPN, SWAPN, EXCHANGE: opcode + 1 byte operand
	opcodeSizes[DUPN] = 2
	opcodeSizes[SWAPN] = 2
	opcodeSizes[EXCHANGE] = 2

	// EOFCREATE, RETURNCONTRACT: opcode + 1 byte container index
	opcodeSizes[EOFCREATE] = 2
	opcodeSizes[RETURNCONTRACT] = 2

	// Note: RJUMPV has variable size and must be handled dynamically
	// (size = 2 + count*2 where count is code[pos+1])
	// We set it to -1 as a marker for dynamic handling
	opcodeSizes[RJUMPV] = -1
}

// GetOpcodeSize returns the total size of an instruction including operands.
// For RJUMPV, the code slice must be provided to read the count byte.
// For all other opcodes, code can be nil.
func GetOpcodeSize(op OpCode, code []byte) int {
	size := int(opcodeSizes[op])

	// Handle RJUMPV specially - it has variable size
	if size == -1 {
		if len(code) > 1 {
			// RJUMPV: opcode + count + count*2 bytes (each entry is 2 bytes)
			return 2 + int(code[1])*2
		}
		return 2 // minimum size when count can't be read
	}

	return size
}

// IsPushOpcode returns true if the opcode is a PUSH instruction.
func IsPushOpcode(op OpCode) bool {
	return op >= PUSH1 && op <= PUSH32
}

// PushSize returns the number of bytes pushed for a PUSH opcode.
// Returns 0 for non-PUSH opcodes.
func PushSize(op OpCode) int {
	if op < PUSH1 || op > PUSH32 {
		return 0
	}
	return int(op - PUSH1 + 1)
}
