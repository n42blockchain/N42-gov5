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

import (
	"errors"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/internal/vm/stack"
	"github.com/n42blockchain/N42/params"
)

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

// =============================================================================
// EIP-8024: Backward compatible SWAPN, DUPN, EXCHANGE (Amsterdam)
// https://eips.ethereum.org/EIPS/eip-8024
//
// Three stack ops with a one-byte immediate extend stack access beyond the
// 16-item DUP/SWAP limit. The immediate ranges are chosen so the byte can
// never be JUMPDEST (0x5b) or collide with PUSH-data masking, which keeps
// legacy jumpdest analysis valid without code versioning. Cost: 3 gas each.
//
// DUPN/SWAPN immediate: valid 0..90 and 128..255, n = (x+145) % 256 in 17..235.
// EXCHANGE immediate: valid 0..81 and 128..255; k = x ^ 143, q,r = divmod(k,16);
// (n,m) = (q+1, r+1) if q < r else (r+1, 29-q).
// =============================================================================

// opDupN implements DUPN (0xe6): duplicates the n'th stack item (1-based) onto
// the top, n decoded from the immediate byte.
func opDupN(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	code := scope.Contract.Code
	if *pc+1 >= uint64(len(code)) {
		return nil, &ErrInvalidOpCode{opcode: DUPN}
	}
	x := code[*pc+1]
	if x > 90 && x < 128 {
		return nil, &ErrInvalidOpCode{opcode: DUPN}
	}
	n := int((uint16(x) + 145) % 256)
	if scope.Stack.Len() < n {
		return nil, &ErrStackUnderflow{stackLen: scope.Stack.Len(), required: n}
	}
	scope.Stack.Dup(n)
	*pc++ // skip immediate
	return nil, nil
}

// opSwapN implements SWAPN (0xe7): swaps the (n+1)'th stack item with the top,
// n decoded from the immediate byte.
func opSwapN(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	code := scope.Contract.Code
	if *pc+1 >= uint64(len(code)) {
		return nil, &ErrInvalidOpCode{opcode: SWAPN}
	}
	x := code[*pc+1]
	if x > 90 && x < 128 {
		return nil, &ErrInvalidOpCode{opcode: SWAPN}
	}
	n := int((uint16(x) + 145) % 256)
	if scope.Stack.Len() < n+1 {
		return nil, &ErrStackUnderflow{stackLen: scope.Stack.Len(), required: n + 1}
	}
	scope.Stack.Swap(n + 1)
	*pc++ // skip immediate
	return nil, nil
}

// opExchange implements EXCHANGE (0xe8): swaps the (n+1)'th and (m+1)'th stack
// items (both below the top), (n,m) decoded from the immediate byte.
func opExchange(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	code := scope.Contract.Code
	if *pc+1 >= uint64(len(code)) {
		return nil, &ErrInvalidOpCode{opcode: EXCHANGE}
	}
	x := code[*pc+1]
	if x > 81 && x < 128 {
		return nil, &ErrInvalidOpCode{opcode: EXCHANGE}
	}
	k := x ^ 143
	q, r := int(k)/16, int(k)%16
	var n, m int
	if q < r {
		n, m = q+1, r+1
	} else {
		n, m = r+1, 29-q
	}
	if scope.Stack.Len() < m+1 {
		return nil, &ErrStackUnderflow{stackLen: scope.Stack.Len(), required: m + 1}
	}
	a, b := scope.Stack.Back(n), scope.Stack.Back(m)
	*a, *b = *b, *a
	*pc++ // skip immediate
	return nil, nil
}

// enable8024 applies EIP-8024 "Backward compatible SWAPN, DUPN and EXCHANGE".
func enable8024(jt *JumpTable) {
	jt[DUPN] = &operation{
		execute:     opDupN,
		constantGas: GasFastestStep,
		numPop:      0,
		numPush:     1,
	}
	jt[SWAPN] = &operation{
		execute:     opSwapN,
		constantGas: GasFastestStep,
		numPop:      0,
		numPush:     0,
	}
	jt[EXCHANGE] = &operation{
		execute:     opExchange,
		constantGas: GasFastestStep,
		numPop:      0,
		numPush:     0,
	}
}

// =============================================================================
// EIP-8037 + EIP-8038: Amsterdam state-gas SSTORE schedule
//
// EIP-8038 replaces the composite SSTORE write costs with an explicit
// STORAGE_WRITE (10000) surcharge and raises the clear refund to 11616.
// EIP-8037 charges CPSB (1530) gas per byte of newly created state: a fresh
// storage slot is 64 bytes -> 97920 gas on top of the write cost.
//
// NOTE: 8037 specifies a two-pool "state-gas reservoir" drawn from the gas
// above TX_MAX_GAS_LIMIT. Until the reservoir parameters freeze on devnet,
// N42 charges state gas directly from execution gas (strict upper bound on
// the reservoir semantics; same total for txs within the execution budget).
// =============================================================================

// gasSStoreAmsterdam mirrors the EIP-2929/2200 SSTORE schedule with the
// Amsterdam constants: STORAGE_WRITE for every real write, CPSB state gas for
// slot creation, and the raised clear refund.
func gasSStoreAmsterdam(evm VMInterpreter, contract *Contract, stack *stack.Stack, mem *Memory, memorySize uint64) (uint64, error) {
	if contract.Gas <= params.SstoreSentryGasEIP2200 {
		return 0, errors.New("not enough gas for reentrancy sentry")
	}
	var (
		y, x    = stack.Back(1), stack.Peek()
		slot    = types.Hash(x.Bytes32())
		current uint256.Int
		cost    = uint64(0)
	)
	const (
		writeGas   = params.StorageWriteEIP8038                                  // 10000
		newSlotGas = params.StateBytesPerStorageSet * params.CostPerStateByteEIP8037 // 97920
		clearRef   = params.StorageClearRefundEIP8038                            // 11616
	)
	evm.IntraBlockState().GetState(contract.Address(), &slot, &current)
	if addrPresent, slotPresent := evm.IntraBlockState().SlotInAccessList(contract.Address(), slot); !slotPresent {
		cost = params.ColdSloadCostEIP2929
		evm.IntraBlockState().AddSlotToAccessList(contract.Address(), slot)
		if !addrPresent {
			return 0, errors.New("impossible case: address was not present in access list during sstore op")
		}
	}
	var value uint256.Int
	value.Set(y)

	if current.Eq(&value) { // noop
		return cost + params.WarmStorageReadCostEIP2929, nil
	}
	var original uint256.Int
	evm.IntraBlockState().GetCommittedState(contract.Address(), &slot, &original)
	if original.Eq(&current) {
		if original.IsZero() { // create slot: write surcharge + state gas
			return cost + writeGas + newSlotGas, nil
		}
		if value.IsZero() { // delete slot
			evm.IntraBlockState().AddRefund(clearRef)
		}
		return cost + writeGas, nil // write existing slot
	}
	if !original.IsZero() {
		if current.IsZero() { // recreate slot
			evm.IntraBlockState().SubRefund(clearRef)
		} else if value.IsZero() { // delete slot
			evm.IntraBlockState().AddRefund(clearRef)
		}
	}
	if original.Eq(&value) {
		if original.IsZero() { // reset to original inexistent slot
			evm.IntraBlockState().AddRefund(writeGas + newSlotGas - params.WarmStorageReadCostEIP2929)
		} else { // reset to original existing slot
			evm.IntraBlockState().AddRefund(writeGas - params.WarmStorageReadCostEIP2929)
		}
	}
	return cost + params.WarmStorageReadCostEIP2929, nil // dirty update
}

// =============================================================================
// EIP-7708: ETH transfers emit a log (Amsterdam)
//
// Every nonzero-value transfer (tx value, CALL family, CREATE endowment,
// SELFDESTRUCT sweep to a different account) emits an ERC-20-style Transfer
// log from the system address. Zero-value transfers, coinbase payments, base
// fee burns and withdrawals are excluded by the spec.
// =============================================================================

// TransferLogAddress is the EIP-7708 system address that emits transfer logs.
var TransferLogAddress = types.HexToAddress("0xfffffffffffffffffffffffffffffffffffffffe")

// transferLogTopic is keccak256("Transfer(address,address,uint256)") — the
// canonical ERC-20 Transfer topic reused by EIP-7708.
var transferLogTopic = types.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")

// EmitTransferLog appends the EIP-7708 log for a nonzero ETH transfer.
// Callers gate on rules.IsGlamsterdam and amount > 0.
func EmitTransferLog(ibs evmtypes.IntraBlockState, from, to types.Address, amount *uint256.Int) {
	var fromTopic, toTopic types.Hash
	copy(fromTopic[12:], from.Bytes())
	copy(toTopic[12:], to.Bytes())
	data := amount.Bytes32()
	ibs.AddLog(&block.Log{
		Address: TransferLogAddress,
		Topics:  []types.Hash{transferLogTopic, fromTopic, toTopic},
		Data:    data[:],
	})
}

func init() {
	activators[7843] = enable7843
	activators[8024] = enable8024
}
