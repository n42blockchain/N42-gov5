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
// Amsterdam fork EIP activation. Introduces EIP-7843, the SLOTNUM
// opcode (0x4b) that pushes the current block's beacon-chain slot onto
// the EVM stack at GasQuickStep. opSlotNum reads BlockContext.Slot from
// the interpreter and returns 0 when unavailable (pre-Merge or when the
// block producer has not populated the field). enable7843 installs the
// operation into a JumpTable and registers itself in activators on
// init.

// Amsterdam EIPs implementation
// Reference: go-ethereum Amsterdam fork
//
// Amsterdam introduces:
// - EIP-7843: SLOTNUM opcode — exposes the beacon chain slot number to the EVM

package vm

// =============================================================================
// EIP-7843: SLOTNUM opcode (Amsterdam/Fusaka)
// https://eips.ethereum.org/EIPS/eip-7843
//
// SLOTNUM (0x4b) pushes the consensus layer slot number for the current block
// onto the stack. The slot number is provided by the beacon chain via the
// Engine API and stored in BlockContext.Slot. Cost: 2 gas (GasQuickStep).
// =============================================================================

// opSlotNum implements SLOTNUM (0x4b).
// Pushes the consensus layer beacon slot number corresponding to the current
// execution block. Returns 0 when the slot is not available (e.g., pre-Merge
// or when BlockContext.Slot has not been populated by the block producer).
func opSlotNum(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	v := scope.Stack.Peek()
	v.SetUint64(interpreter.evm.Context().Slot)
	return nil, nil
}

// enable7843 applies EIP-7843 "SLOTNUM opcode".
// Adds the SLOTNUM (0x4b) opcode to the jump table with GasQuickStep (2 gas).
func enable7843(jt *JumpTable) {
	jt[SLOTNUM] = &operation{
		execute:     opSlotNum,
		constantGas: GasQuickStep,
		numPop:      0,
		numPush:     1,
	}
}

func init() {
	activators[7843] = enable7843
}
