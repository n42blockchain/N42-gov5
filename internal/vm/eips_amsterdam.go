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
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
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
	scope.Stack.Push(new(uint256.Int).SetUint64(interpreter.evm.Context().Slot))
	return nil, nil
}

// opSlotNumFusaka is SLOTNUM as the N42 Fusaka fork has executed it since
// activation: it writes the slot into the current stack top instead of
// pushing it. It is kept, frozen, so blocks of N42 chains past FusakaTime
// replay unchanged; the Ethereum Amsterdam table uses opSlotNum.
//
// On an empty stack it used to dereference nil and panic the node, so no
// committed block can have executed that case; it is a stack underflow now.
func opSlotNumFusaka(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	if scope.Stack.Len() == 0 {
		return nil, &ErrStackUnderflow{stackLen: 0, required: 1}
	}
	v := scope.Stack.Peek()
	v.SetUint64(interpreter.evm.Context().Slot)
	return nil, nil
}

// enable7843Fusaka installs SLOTNUM with its N42 Fusaka semantics
// (opSlotNumFusaka).
func enable7843Fusaka(jt *JumpTable) {
	enable7843(jt)
	jt[SLOTNUM].execute = opSlotNumFusaka
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

// immediateByte returns the immediate operand of the instruction at pc. A
// missing immediate (the instruction is the last byte of the code) reads as
// 0x00, the same convention as PUSHn data.
func immediateByte(code []byte, pc uint64) byte {
	if pc+1 < uint64(len(code)) {
		return code[pc+1]
	}
	return 0
}

// opDupN implements DUPN (0xe6): duplicates the n'th stack item (1-based) onto
// the top, n decoded from the immediate byte.
func opDupN(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	x := immediateByte(scope.Contract.Code, *pc)
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
	x := immediateByte(scope.Contract.Code, *pc)
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
	x := immediateByte(scope.Contract.Code, *pc)
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
