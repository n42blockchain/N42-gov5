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
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/stack"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// EIP-1153: Transient Storage (Cancun)
// https://eips.ethereum.org/EIPS/eip-1153
// =============================================================================

// enable1153 applies EIP-1153 "Transient Storage"
// - Adds TLOAD (0x5c) - transient storage load
// - Adds TSTORE (0x5d) - transient storage store
func enable1153(jt *JumpTable) {
	jt[TLOAD] = &operation{
		execute:     opTload,
		constantGas: params.WarmStorageReadCostEIP2929,
		numPop:      1,
		numPush:     1,
	}

	jt[TSTORE] = &operation{
		execute:     opTstore,
		constantGas: params.WarmStorageReadCostEIP2929,
		numPop:      2,
		numPush:     0,
	}
}

// opTload implements TLOAD (0x5c)
func opTload(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	loc := scope.Stack.Peek()
	hash := types.Hash(loc.Bytes32())
	val := interpreter.evm.IntraBlockState().GetTransientState(scope.Contract.Address(), hash)
	loc.Set(&val)
	return nil, nil
}

// opTstore implements TSTORE (0x5d)
func opTstore(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	if interpreter.readOnly {
		return nil, ErrWriteProtection
	}
	loc := scope.Stack.Pop()
	val := scope.Stack.Pop()
	interpreter.evm.IntraBlockState().SetTransientState(scope.Contract.Address(), types.Hash(loc.Bytes32()), val)
	return nil, nil
}

// =============================================================================
// EIP-5656: MCOPY - Memory copying instruction (Cancun)
// https://eips.ethereum.org/EIPS/eip-5656
// =============================================================================

// enable5656 applies EIP-5656 "MCOPY - Memory copying instruction"
// - Adds MCOPY (0x5e) - efficient memory copy
func enable5656(jt *JumpTable) {
	jt[MCOPY] = &operation{
		execute:     opMcopy,
		constantGas: GasFastestStep,
		dynamicGas:  gasMcopy,
		numPop:      3,
		numPush:     0,
		memorySize:  memoryMcopy,
	}
}

// opMcopy implements MCOPY (0x5e)
func opMcopy(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	var (
		dst    = scope.Stack.Pop()
		src    = scope.Stack.Pop()
		length = scope.Stack.Pop()
	)
	// Copy data within memory
	scope.Memory.Copy(dst.Uint64(), src.Uint64(), length.Uint64())
	return nil, nil
}

// gasMcopy calculates the gas cost for MCOPY
func gasMcopy(evm VMInterpreter, contract *Contract, stk *stack.Stack, mem *Memory, memorySize uint64) (uint64, error) {
	gas, err := memoryGasCost(mem, memorySize)
	if err != nil {
		return 0, err
	}
	// Calculate word size for copy cost
	words, overflow := stk.Back(2).Uint64WithOverflow()
	if overflow {
		return 0, ErrGasUintOverflow
	}
	if words, overflow = safeMul(toWordSize(words), params.CopyGas); overflow {
		return 0, ErrGasUintOverflow
	}
	if gas, overflow = safeAdd(gas, words); overflow {
		return 0, ErrGasUintOverflow
	}
	return gas, nil
}

// memoryMcopy calculates the memory size for MCOPY
func memoryMcopy(stk *stack.Stack) (uint64, bool) {
	mStart := stk.Back(0)
	mEnd := stk.Back(1)
	mLength := stk.Back(2)

	// Calculate destination end
	dstEnd := new(uint256.Int).Add(mStart, mLength)
	// Calculate source end
	srcEnd := new(uint256.Int).Add(mEnd, mLength)

	// Return the maximum of dst end and src end
	if dstEnd.Cmp(srcEnd) > 0 {
		return calcMemSize64(mStart, mLength)
	}
	return calcMemSize64(mEnd, mLength)
}

// =============================================================================
// EIP-4844: Shard Blob Transactions (Cancun)
// https://eips.ethereum.org/EIPS/eip-4844
// =============================================================================

// enable4844 applies EIP-4844 "Shard Blob Transactions"
// - Adds BLOBHASH (0x49) - get versioned blob hash
func enable4844(jt *JumpTable) {
	jt[BLOBHASH] = &operation{
		execute:     opBlobHash,
		constantGas: GasFastestStep,
		numPop:      1,
		numPush:     1,
	}
}

// opBlobHash implements BLOBHASH (0x49)
func opBlobHash(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	index := scope.Stack.Peek()
	if index.LtUint64(uint64(len(interpreter.evm.TxContext().BlobHashes))) {
		blobHash := interpreter.evm.TxContext().BlobHashes[index.Uint64()]
		index.SetBytes32(blobHash[:])
	} else {
		index.Clear()
	}
	return nil, nil
}

// =============================================================================
// EIP-7516: BLOBBASEFEE opcode (Cancun)
// https://eips.ethereum.org/EIPS/eip-7516
// =============================================================================

// enable7516 applies EIP-7516 "BLOBBASEFEE opcode"
// - Adds BLOBBASEFEE (0x4a) - get current block's blob base fee
func enable7516(jt *JumpTable) {
	jt[BLOBBASEFEE] = &operation{
		execute:     opBlobBaseFee,
		constantGas: GasQuickStep,
		numPop:      0,
		numPush:     1,
	}
}

// opBlobBaseFee implements BLOBBASEFEE (0x4a)
func opBlobBaseFee(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	blobBaseFee := interpreter.evm.Context().BlobBaseFee
	if blobBaseFee == nil {
		scope.Stack.Push(new(uint256.Int))
	} else {
		scope.Stack.Push(new(uint256.Int).Set(blobBaseFee))
	}
	return nil, nil
}

// =============================================================================
// EIP-6780: SELFDESTRUCT only in same transaction (Cancun)
// https://eips.ethereum.org/EIPS/eip-6780
// =============================================================================

// enable6780 applies EIP-6780 "SELFDESTRUCT only in same transaction"
// The opcode behavior changes are handled in the opSelfdestruct function
// by checking if the contract was created in the same transaction.
func enable6780(jt *JumpTable) {
	jt[SELFDESTRUCT].dynamicGas = gasSelfdestructEIP6780
}

// gasSelfdestructEIP6780 calculates gas for SELFDESTRUCT under EIP-6780
func gasSelfdestructEIP6780(evm VMInterpreter, contract *Contract, stk *stack.Stack, mem *Memory, memorySize uint64) (uint64, error) {
	var (
		gas     uint64
		address = types.Address(stk.Back(0).Bytes20())
	)
	if !evm.IntraBlockState().AddressInAccessList(address) {
		// If the caller cannot be charged (call depth too low), then revert
		gas = params.ColdAccountAccessCostEIP2929
		// If the caller is charged, the warm storage read is already charged as constantGas
		evm.IntraBlockState().AddAddressToAccessList(address)
	}
	return gas, nil
}

func init() {
	// Register Cancun EIPs
	activators[1153] = enable1153
	activators[5656] = enable5656
	activators[4844] = enable4844
	activators[7516] = enable7516
	activators[6780] = enable6780
}

