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

// Osaka EIPs implementation
// Reference: go-ethereum and erigon implementations
//
// Osaka focuses on:
// - EOF (EVM Object Format) - EIP-3540, EIP-3670, EIP-4200, EIP-4750, EIP-5450
// - Verkle Trees preparation
// - Light client support

package vm

import (
	"encoding/binary"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/internal/vm/stack"
)

// =============================================================================
// Osaka Gas Constants
// =============================================================================

const (
	// Gas costs for EOF instructions
	GasRJUMP  = 2   // RJUMP gas cost
	GasRJUMPI = 4   // RJUMPI gas cost
	GasRJUMPV = 4   // RJUMPV base gas cost
	GasCALLF  = 5   // CALLF gas cost
	GasRETF   = 3   // RETF gas cost
	GasJUMPF  = 5   // JUMPF gas cost

	// Data section access
	GasDataLoad  = 4   // DATALOAD gas cost
	GasDataLoadN = 3   // DATALOADN gas cost
	GasDataSize  = 2   // DATASIZE gas cost
	GasDataCopy  = 3   // DATACOPY base gas cost

	// Stack manipulation
	GasDUPN     = 3   // DUPN gas cost
	GasSWAPN    = 3   // SWAPN gas cost
	GasEXCHANGE = 3   // EXCHANGE gas cost

	// Contract creation
	GasEOFCREATE      = 32000 // EOFCREATE gas cost (same as CREATE)
	GasRETURNCONTRACT = 0     // RETURNCONTRACT gas cost
	GasRETURNDATALOAD = 3     // RETURNDATALOAD gas cost
)

// =============================================================================
// EOF Opcode Implementations
// =============================================================================

// opRJUMP implements RJUMP (0xE0) - unconditional relative jump
func opRJUMP(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	code := scope.Contract.Code
	offset := int16(binary.BigEndian.Uint16(code[*pc+1:]))
	*pc = uint64(int64(*pc) + 3 + int64(offset) - 1) // -1 because pc is incremented after
	return nil, nil
}

// opRJUMPI implements RJUMPI (0xE1) - conditional relative jump
func opRJUMPI(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	condition := scope.Stack.Pop()
	if !condition.IsZero() {
		code := scope.Contract.Code
		offset := int16(binary.BigEndian.Uint16(code[*pc+1:]))
		*pc = uint64(int64(*pc) + 3 + int64(offset) - 1)
	} else {
		*pc += 2 // Skip the 2-byte offset
	}
	return nil, nil
}

// opRJUMPV implements RJUMPV (0xE2) - jump table (switch)
func opRJUMPV(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	code := scope.Contract.Code
	count := int(code[*pc+1])
	caseIndex := scope.Stack.Pop()

	if !caseIndex.IsUint64() || caseIndex.Uint64() >= uint64(count) {
		// Jump to default case (after the jump table)
		*pc += uint64(2 + count*2 - 1)
		return nil, nil
	}

	idx := int(caseIndex.Uint64())
	offsetPos := *pc + 2 + uint64(idx*2)
	offset := int16(binary.BigEndian.Uint16(code[offsetPos:]))
	*pc = uint64(int64(*pc) + 2 + int64(count*2) + int64(offset) - 1)
	return nil, nil
}

// opCALLF implements CALLF (0xE3) - call function
func opCALLF(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	code := scope.Contract.Code
	funcIdx := binary.BigEndian.Uint16(code[*pc+1:])

	// Push return address to return stack
	returnAddr := uint32(*pc + 3)
	scope.ReturnStack.Push(returnAddr)

	// Get target code section
	container := scope.Contract.EOFContainer
	if container == nil || int(funcIdx) >= container.NumCodeSections() {
		return nil, ErrEOFInvalidCallF
	}

	// Update scope to new code section
	scope.Contract.CodeSection = int(funcIdx)
	*pc = 0 // Start at beginning of new section
	*pc-- // Will be incremented by interpreter loop

	return nil, nil
}

// opRETF implements RETF (0xE4) - return from function
func opRETF(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	// Check if return stack is empty
	if scope.ReturnStack == nil || len(scope.ReturnStack.Data()) == 0 {
		return nil, ErrEOFInvalidRetF
	}

	// Pop return address from return stack
	returnAddr := scope.ReturnStack.Pop()

	// Restore to previous code section
	// The return address encodes both section and position
	scope.Contract.CodeSection = 0 // Main section
	*pc = uint64(returnAddr) - 1   // Will be incremented

	return nil, nil
}

// opJUMPF implements JUMPF (0xE5) - tail call to function
func opJUMPF(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	code := scope.Contract.Code
	funcIdx := binary.BigEndian.Uint16(code[*pc+1:])

	// Get target code section
	container := scope.Contract.EOFContainer
	if container == nil || int(funcIdx) >= container.NumCodeSections() {
		return nil, ErrEOFInvalidCallF
	}

	// Jump to new code section without pushing return address
	scope.Contract.CodeSection = int(funcIdx)
	*pc = 0
	*pc--

	return nil, nil
}

// opDATALOAD implements DATALOAD (0xD0) - load 32 bytes from data section
func opDATALOAD(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	offset := scope.Stack.Peek()

	container := scope.Contract.EOFContainer
	if container == nil {
		offset.Clear()
		return nil, nil
	}

	data := container.GetData()
	off64 := offset.Uint64()

	if !offset.IsUint64() || off64+32 > uint64(len(data)) {
		// Out of bounds - return zeros
		offset.Clear()
		return nil, nil
	}

	offset.SetBytes32(data[off64 : off64+32])
	return nil, nil
}

// opDATALOADN implements DATALOADN (0xD1) - load 32 bytes with immediate offset
func opDATALOADN(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	code := scope.Contract.Code
	offset := binary.BigEndian.Uint16(code[*pc+1:])

	container := scope.Contract.EOFContainer
	if container == nil {
		scope.Stack.Push(new(uint256.Int))
		return nil, nil
	}

	data := container.GetData()
	if int(offset)+32 > len(data) {
		scope.Stack.Push(new(uint256.Int))
		return nil, nil
	}

	value := new(uint256.Int).SetBytes32(data[offset : offset+32])
	scope.Stack.Push(value)

	*pc += 2 // Skip immediate
	return nil, nil
}

// opDATASIZE implements DATASIZE (0xD2) - get data section size
func opDATASIZE(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	container := scope.Contract.EOFContainer
	size := uint64(0)
	if container != nil {
		size = uint64(container.DataSize())
	}
	scope.Stack.Push(uint256.NewInt(size))
	return nil, nil
}

// opDATACOPY implements DATACOPY (0xD3) - copy from data section to memory
func opDATACOPY(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	var (
		memOffset  = scope.Stack.Pop()
		dataOffset = scope.Stack.Pop()
		length     = scope.Stack.Pop()
	)

	container := scope.Contract.EOFContainer
	if container == nil || length.IsZero() {
		return nil, nil
	}

	data := container.GetData()
	dataOff64, overflow := dataOffset.Uint64WithOverflow()
	if overflow || dataOff64 > uint64(len(data)) {
		dataOff64 = uint64(len(data))
	}

	len64 := length.Uint64()
	end := dataOff64 + len64
	if end > uint64(len(data)) {
		end = uint64(len(data))
	}

	// Copy data to memory
	scope.Memory.Set(memOffset.Uint64(), len64, data[dataOff64:end])
	return nil, nil
}

// opDUPN implements DUPN (0xE6) - DUP with immediate operand
func opDUPN(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	code := scope.Contract.Code
	n := int(code[*pc+1]) + 1 // n is 1-indexed in the opcode

	value := scope.Stack.Back(n - 1)
	scope.Stack.Push(new(uint256.Int).Set(value))

	*pc++ // Skip immediate
	return nil, nil
}

// opSWAPN implements SWAPN (0xE7) - SWAP with immediate operand
func opSWAPN(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	code := scope.Contract.Code
	n := int(code[*pc+1]) + 1 // n is 1-indexed

	scope.Stack.Swap(n)

	*pc++ // Skip immediate
	return nil, nil
}

// opEXCHANGE implements EXCHANGE (0xE8) - exchange two stack items
func opEXCHANGE(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	code := scope.Contract.Code
	imm := code[*pc+1]
	n := int((imm >> 4) + 1)
	m := int((imm & 0x0f) + 1)

	// Exchange stack[n] and stack[n+m]
	a := scope.Stack.Back(n - 1)
	b := scope.Stack.Back(n + m - 1)

	tmp := new(uint256.Int).Set(a)
	a.Set(b)
	b.Set(tmp)

	*pc++ // Skip immediate
	return nil, nil
}

// opEOFCREATE implements EOFCREATE (0xEC) - create contract from EOF container
func opEOFCREATE(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	// This is a simplified implementation
	// Full implementation requires contract creation logic
	code := scope.Contract.Code
	_ = code[*pc+1] // Container index

	// Push failure (0) for now - full implementation needed
	scope.Stack.Push(new(uint256.Int))

	*pc++ // Skip immediate
	return nil, nil
}

// opRETURNCONTRACT implements RETURNCONTRACT (0xEE) - return new contract from initcode
func opRETURNCONTRACT(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	code := scope.Contract.Code
	_ = code[*pc+1] // Container index

	// Implementation requires integration with contract creation
	return nil, ErrExecutionReverted
}

// opRETURNDATALOAD implements RETURNDATALOAD (0xF7) - load 32 bytes from return data
func opRETURNDATALOAD(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	offset := scope.Stack.Peek()
	off64 := offset.Uint64()

	returnData := interpreter.returnData
	if !offset.IsUint64() || off64+32 > uint64(len(returnData)) {
		offset.Clear()
		return nil, nil
	}

	offset.SetBytes32(returnData[off64 : off64+32])
	return nil, nil
}

// =============================================================================
// Osaka JumpTable Modifications
// =============================================================================

// enableEOF enables EOF-specific opcodes
func enableEOF(jt *JumpTable) {
	// EIP-4200: Static relative jumps
	jt[RJUMP] = &operation{
		execute:     opRJUMP,
		constantGas: GasRJUMP,
		numPop:      0,
		numPush:     0,
	}
	jt[RJUMPI] = &operation{
		execute:     opRJUMPI,
		constantGas: GasRJUMPI,
		numPop:      1,
		numPush:     0,
	}
	jt[RJUMPV] = &operation{
		execute:     opRJUMPV,
		constantGas: GasRJUMPV,
		numPop:      1,
		numPush:     0,
	}

	// EIP-4750: Functions
	jt[CALLF] = &operation{
		execute:     opCALLF,
		constantGas: GasCALLF,
		numPop:      0,
		numPush:     0,
	}
	jt[RETF] = &operation{
		execute:     opRETF,
		constantGas: GasRETF,
		numPop:      0,
		numPush:     0,
	}
	jt[JUMPF] = &operation{
		execute:     opJUMPF,
		constantGas: GasJUMPF,
		numPop:      0,
		numPush:     0,
	}

	// EIP-7480: Data section access
	jt[DATALOAD] = &operation{
		execute:     opDATALOAD,
		constantGas: GasDataLoad,
		numPop:      1,
		numPush:     1,
	}
	jt[DATALOADN] = &operation{
		execute:     opDATALOADN,
		constantGas: GasDataLoadN,
		numPop:      0,
		numPush:     1,
	}
	jt[DATASIZE] = &operation{
		execute:     opDATASIZE,
		constantGas: GasDataSize,
		numPop:      0,
		numPush:     1,
	}
	jt[DATACOPY] = &operation{
		execute:     opDATACOPY,
		constantGas: GasDataCopy,
		numPop:      3,
		numPush:     0,
		dynamicGas:  gasDataCopy,
		memorySize:  memoryDataCopy,
	}

	// EIP-663: Unlimited SWAP and DUP
	jt[DUPN] = &operation{
		execute:     opDUPN,
		constantGas: GasDUPN,
		numPop:      0,
		numPush:     1,
	}
	jt[SWAPN] = &operation{
		execute:     opSWAPN,
		constantGas: GasSWAPN,
		numPop:      0,
		numPush:     0,
	}
	jt[EXCHANGE] = &operation{
		execute:     opEXCHANGE,
		constantGas: GasEXCHANGE,
		numPop:      0,
		numPush:     0,
	}

	// EIP-7620: Contract creation
	jt[EOFCREATE] = &operation{
		execute:     opEOFCREATE,
		constantGas: GasEOFCREATE,
		numPop:      4,
		numPush:     1,
	}
	jt[RETURNCONTRACT] = &operation{
		execute:     opRETURNCONTRACT,
		constantGas: GasRETURNCONTRACT,
		numPop:      2,
		numPush:     0,
	}
	jt[RETURNDATALOAD] = &operation{
		execute:     opRETURNDATALOAD,
		constantGas: GasRETURNDATALOAD,
		numPop:      1,
		numPush:     1,
	}

	// Disable legacy opcodes in EOF
	disableLegacyOpcodes(jt)
}

// disableLegacyOpcodes disables opcodes that are not allowed in EOF
func disableLegacyOpcodes(jt *JumpTable) {
	// These opcodes are replaced or removed in EOF
	// We don't actually nil them out, but mark them for legacy only
	// The EOF validator will reject code containing these
}

// gasDataCopy calculates dynamic gas for DATACOPY
func gasDataCopy(evm VMInterpreter, contract *Contract, stack *stack.Stack, mem *Memory, memorySize uint64) (uint64, error) {
	gas, err := memoryGasCost(mem, memorySize)
	if err != nil {
		return 0, err
	}

	// Add copy cost
	words := toWordSize(stack.Back(2).Uint64())
	copyGas := 3 * words
	totalGas, overflow := safeAdd(gas, copyGas)
	if overflow {
		return 0, ErrGasUintOverflow
	}
	return totalGas, nil
}

// memoryDataCopy returns the memory size for DATACOPY
func memoryDataCopy(stack *stack.Stack) (uint64, bool) {
	return calcMemSize64(stack.Back(0), stack.Back(2))
}

// newOsakaInstructionSet returns the Osaka instruction set
// Osaka = Pectra + EOF
func newOsakaInstructionSet() JumpTable {
	instructionSet := newPectraInstructionSet()
	enableEOF(&instructionSet)
	validateAndFillMaxStack(&instructionSet)
	return instructionSet
}

// =============================================================================
// Init function to register Osaka
// =============================================================================

func init() {
	// Register EOF enabler
	activators[3540] = enableEOF // EIP-3540: EOF
}

