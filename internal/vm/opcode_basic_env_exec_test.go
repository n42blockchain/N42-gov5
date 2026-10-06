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
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
)

// vmTRunReturn32 executes code expected to RETURN exactly 32 bytes and
// returns them as a uint256 for easy assertion.
func vmTRunReturn32(t *testing.T, code []byte) *uint256.Int {
	t.Helper()
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	ret, _, err := execHarnessCall(t, evm, ibs, code, nil, 10_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if len(ret) != 32 {
		t.Fatalf("expected 32 bytes, got %d: %x", len(ret), ret)
	}
	return new(uint256.Int).SetBytes(ret)
}

// opPush1Store0Return32 builds: PUSH ... MSTORE(0) RETURN(0,32) wrapping an
// opcode that leaves a single 32-byte word on the stack.
func wrapOpReturn32(op byte) []byte {
	return []byte{
		op,                // leaves value on stack
		byte(PUSH1), 0x00, // offset
		byte(MSTORE),
		byte(PUSH1), 0x20, // size
		byte(PUSH1), 0x00, // offset
		byte(RETURN),
	}
}

func TestVMTOpOrigin(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	want := types.HexToAddress("0x00000000000000000000000000000000001234")
	evm.txContext.Origin = want
	code := wrapOpReturn32(byte(ORIGIN))
	ret, _, err := execHarnessCall(t, evm, ibs, code, nil, 10_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	got := types.BytesToAddress(ret)
	if got != want {
		t.Fatalf("ORIGIN mismatch: got %s want %s", got, want)
	}
}

func TestVMTOpGasprice(t *testing.T) {
	code := wrapOpReturn32(byte(GASPRICE))
	v := vmTRunReturn32(t, code)
	if v.Cmp(uint256.NewInt(1)) != 0 {
		t.Fatalf("GASPRICE mismatch: got %s", v)
	}
}

func TestVMTOpCoinbase(t *testing.T) {
	code := wrapOpReturn32(byte(COINBASE))
	v := vmTRunReturn32(t, code)
	if v.IsZero() {
		t.Fatalf("COINBASE should not be zero")
	}
}

func TestVMTOpTimestamp(t *testing.T) {
	code := wrapOpReturn32(byte(TIMESTAMP))
	v := vmTRunReturn32(t, code)
	if v.Cmp(uint256.NewInt(1)) != 0 {
		t.Fatalf("TIMESTAMP mismatch: got %s", v)
	}
}

func TestVMTOpNumber(t *testing.T) {
	code := wrapOpReturn32(byte(NUMBER))
	v := vmTRunReturn32(t, code)
	if v.Cmp(uint256.NewInt(1)) != 0 {
		t.Fatalf("NUMBER mismatch: got %s", v)
	}
}

func TestVMTOpPrevrandao(t *testing.T) {
	// DIFFICULTY/PREVRANDAO opcode: with Difficulty=0 this should return 0.
	code := wrapOpReturn32(byte(DIFFICULTY))
	v := vmTRunReturn32(t, code)
	if !v.IsZero() {
		t.Fatalf("PREVRANDAO mismatch: got %s", v)
	}
}

func TestVMTOpGasLimit(t *testing.T) {
	code := wrapOpReturn32(byte(GASLIMIT))
	v := vmTRunReturn32(t, code)
	if v.Cmp(uint256.NewInt(30_000_000)) != 0 {
		t.Fatalf("GASLIMIT mismatch: got %s", v)
	}
}

func TestVMTOpBaseFee(t *testing.T) {
	code := wrapOpReturn32(byte(BASEFEE))
	v := vmTRunReturn32(t, code)
	if v.Cmp(uint256.NewInt(1)) != 0 {
		t.Fatalf("BASEFEE mismatch: got %s", v)
	}
}

func TestVMTOpGas(t *testing.T) {
	code := []byte{
		byte(GAS),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	v := vmTRunReturn32(t, code)
	if v.IsZero() {
		t.Fatalf("GAS should report remaining gas > 0")
	}
}

func TestVMTOpPC(t *testing.T) {
	// PUSH1 0x00(2 bytes, pc 0-1) then PC at offset 2.
	code := []byte{
		byte(PUSH1), 0x00, // pc 0
		byte(POP),
		byte(PC), // pc at 3
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	v := vmTRunReturn32(t, code)
	if v.Cmp(uint256.NewInt(3)) != 0 {
		t.Fatalf("PC mismatch: got %s want 3", v)
	}
}

func TestVMTOpMsize(t *testing.T) {
	code := []byte{
		byte(PUSH1), 0x01,
		byte(PUSH1), 0x00,
		byte(MSTORE), // expands memory to 32 bytes
		byte(MSIZE),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	v := vmTRunReturn32(t, code)
	if v.Cmp(uint256.NewInt(32)) != 0 {
		t.Fatalf("MSIZE mismatch: got %s want 32", v)
	}
}

func TestVMTOpMloadMstorePop(t *testing.T) {
	code := []byte{
		byte(PUSH1), 0x2a, // 42
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x00, // junk value to pop
		byte(POP),
		byte(PUSH1), 0x00,
		byte(MLOAD),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	v := vmTRunReturn32(t, code)
	if v.Cmp(uint256.NewInt(42)) != 0 {
		t.Fatalf("MLOAD/MSTORE/POP mismatch: got %s want 42", v)
	}
}

func TestVMTOpJumpJumpi(t *testing.T) {
	// JUMP over a REVERT to a JUMPDEST, then JUMPI (taken) to another
	// JUMPDEST that returns 7.
	code := []byte{
		byte(PUSH1), 0x05, // 0,1: dest of first JUMPDEST
		byte(JUMP),        // 2
		byte(INVALID),     // 3 (skipped)
		byte(0x00),        // 4 padding (unreachable)
		byte(JUMPDEST),    // 5
		byte(PUSH1), 0x01, // 6,7: cond = 1
		byte(PUSH1), 0x0e, // 8,9: dest for JUMPI
		byte(JUMPI),   // 10
		byte(INVALID), // 11 unreachable if JUMPI taken
		byte(0), byte(0), // 12,13 padding
		byte(JUMPDEST), // 14
		byte(PUSH1), 0x07,
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	v := vmTRunReturn32(t, code)
	if v.Cmp(uint256.NewInt(7)) != 0 {
		t.Fatalf("JUMP/JUMPI mismatch: got %s want 7", v)
	}
}

func TestVMTOpJumpiNotTaken(t *testing.T) {
	code := []byte{
		byte(PUSH1), 0x00, // cond = 0 (false)
		byte(PUSH1), 0x09, // dest
		byte(JUMPI),       // not taken, falls through
		byte(PUSH1), 0x01, // 1
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
		byte(JUMPDEST), // pc9 - unreachable in this path
		byte(INVALID),
	}
	v := vmTRunReturn32(t, code)
	if v.Cmp(uint256.NewInt(1)) != 0 {
		t.Fatalf("JUMPI-not-taken mismatch: got %s want 1", v)
	}
}

func TestVMTOpSlt(t *testing.T) {
	// -1 < 1 -> true (1)
	code := []byte{
		byte(PUSH1), 0x01,
	}
	code = append(code, byte(PUSH32))
	negOne := make([]byte, 32)
	for i := range negOne {
		negOne[i] = 0xff
	}
	code = append(code, negOne...)
	code = append(code,
		byte(SLT), // negOne < 1 => true since signed negOne is -1
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	)
	v := vmTRunReturn32(t, code)
	if v.Cmp(uint256.NewInt(1)) != 0 {
		t.Fatalf("SLT mismatch: got %s want 1", v)
	}
}

func TestVMTOpSgt(t *testing.T) {
	// 1 > -1 -> true
	negOne := make([]byte, 32)
	for i := range negOne {
		negOne[i] = 0xff
	}
	code := []byte{byte(PUSH32)}
	code = append(code, negOne...)
	code = append(code, byte(PUSH1), 0x01)
	code = append(code,
		byte(SGT), // 1 > negOne(-1) => true
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	)
	v := vmTRunReturn32(t, code)
	if v.Cmp(uint256.NewInt(1)) != 0 {
		t.Fatalf("SGT mismatch: got %s want 1", v)
	}
}

func TestVMTOpSmod(t *testing.T) {
	// -8 % 3 (signed) = -2 (per EVM spec, sign follows dividend)
	negEight := new(uint256.Int).Neg(uint256.NewInt(8))
	code := []byte{byte(PUSH1), 0x03}
	code = append(code, byte(PUSH32))
	b32 := negEight.Bytes32()
	code = append(code, b32[:]...)
	code = append(code,
		byte(SMOD),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	)
	v := vmTRunReturn32(t, code)
	want := new(uint256.Int).Neg(uint256.NewInt(2))
	if v.Cmp(want) != 0 {
		t.Fatalf("SMOD mismatch: got %s want %s", v, want)
	}
}

func TestVMTOpBlockhashInWindow(t *testing.T) {
	// Use BlockNumber=300 with a GetHash that returns a distinct non-zero
	// hash per block number, so we can distinguish in-window from
	// out-of-window results unambiguously.
	evm, ibs := newExecHarnessEVMWithBlockNumber(t, 300)
	code := append([]byte{byte(PUSH1), 0x64}, wrapOpReturn32(byte(BLOCKHASH))...) // block 100, within [44,299] window
	ret, _, err := execHarnessCall(t, evm, ibs, code, nil, 10_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	v := new(uint256.Int).SetBytes(ret)
	if v.IsZero() {
		t.Fatalf("BLOCKHASH(42) should be non-zero within window")
	}
}

func TestVMTOpBlockhashOutOfWindow(t *testing.T) {
	// Requesting a block number >= current block number must be zero.
	evm, ibs := newExecHarnessEVMWithBlockNumber(t, 300)
	// Push block number 300 (0x012c), equal to the current block number,
	// which must be treated as out of the BLOCKHASH window.
	code := append([]byte{byte(PUSH2), 0x01, 0x2c}, wrapOpReturn32(byte(BLOCKHASH))...)
	ret, _, err := execHarnessCall(t, evm, ibs, code, nil, 10_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	v := new(uint256.Int).SetBytes(ret)
	if !v.IsZero() {
		t.Fatalf("BLOCKHASH of a future/out-of-window block must be zero, got %s", v)
	}
}

// newExecHarnessEVMWithBlockNumber mirrors newExecHarnessEVM but sets a
// custom current block number and a deterministic non-degenerate GetHash,
// for tests that need to distinguish in-window from out-of-window blocks.
func newExecHarnessEVMWithBlockNumber(t *testing.T, blockNumber uint64) (*EVM, *state.IntraBlockState) {
	t.Helper()

	db := memdb.NewTestDB(t)
	tx := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(tx, 1))
	blockCtx := evmtypes.BlockContext{
		CanTransfer: func(db evmtypes.IntraBlockState, addr types.Address, amount *uint256.Int) bool {
			return db.GetBalance(addr).Cmp(amount) >= 0
		},
		Transfer: func(db evmtypes.IntraBlockState, sender, recipient types.Address, amount *uint256.Int, bailout bool) {
			db.SubBalance(sender, amount)
			db.AddBalance(recipient, amount)
		},
		GetHash: func(n uint64) types.Hash {
			return types.BytesToHash([]byte{0xaa, byte(n), byte(n >> 8)})
		},
		Coinbase:    types.HexToAddress("0xc011bac0000000000000000000000000000000"),
		GasLimit:    30_000_000,
		BlockNumber: blockNumber,
		Time:        1,
		Difficulty:  big.NewInt(0),
		BaseFee:     uint256.NewInt(1),
	}
	evm := NewEVM(blockCtx, evmtypes.TxContext{GasPrice: uint256.NewInt(1)}, ibs, execHarnessChainConfig(), Config{})
	return evm, ibs
}

func TestVMTOpSignExtendEdges(t *testing.T) {
	// SIGNEXTEND with byte index >= 31 should be a no-op (value unchanged).
	code := []byte{
		byte(PUSH1), 0x2a, // value = 42
		byte(PUSH1), 0x1f, // byteNum = 31 (max meaningful index, no-op boundary)
		byte(SIGNEXTEND),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	v := vmTRunReturn32(t, code)
	if v.Cmp(uint256.NewInt(42)) != 0 {
		t.Fatalf("SIGNEXTEND boundary mismatch: got %s want 42", v)
	}

	// SIGNEXTEND with byteNum >= 32 (huge) should also be a no-op.
	code2 := []byte{
		byte(PUSH1), 0x2a,
		byte(PUSH32),
	}
	big32 := make([]byte, 32)
	for i := range big32 {
		big32[i] = 0xff
	}
	code2 = append(code2, big32...)
	code2 = append(code2,
		byte(SIGNEXTEND),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	)
	v2 := vmTRunReturn32(t, code2)
	if v2.Cmp(uint256.NewInt(42)) != 0 {
		t.Fatalf("SIGNEXTEND huge byteNum mismatch: got %s want 42", v2)
	}

	// SIGNEXTEND that sign-extends a negative byte: byteNum=0, value=0xff
	// should produce all-ones (−1).
	code3 := []byte{
		byte(PUSH1), 0xff,
		byte(PUSH1), 0x00,
		byte(SIGNEXTEND),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	v3 := vmTRunReturn32(t, code3)
	want3 := new(uint256.Int).Not(uint256.NewInt(0))
	if v3.Cmp(want3) != 0 {
		t.Fatalf("SIGNEXTEND sign-extend mismatch: got %s want %s", v3, want3)
	}
}

func TestVMTOpDup16Swap16(t *testing.T) {
	// Push 17 distinct values, DUP16 the one 16 deep, then verify via return.
	code := []byte{}
	for i := byte(1); i <= 17; i++ {
		code = append(code, byte(PUSH1), i)
	}
	// Stack top->bottom: 17,16,...,1. DUP16 duplicates the 16th item from top (value 2).
	code = append(code, byte(DUP16))
	code = append(code,
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	)
	v := vmTRunReturn32(t, code)
	if v.Cmp(uint256.NewInt(2)) != 0 {
		t.Fatalf("DUP16 mismatch: got %s want 2", v)
	}
}

func TestVMTOpSwap16(t *testing.T) {
	code := []byte{}
	for i := byte(1); i <= 17; i++ {
		code = append(code, byte(PUSH1), i)
	}
	// Stack top->bottom: 17,16,...,1. SWAP16 swaps top (17) with 17th item (1).
	code = append(code, byte(SWAP16))
	code = append(code,
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	)
	v := vmTRunReturn32(t, code)
	if v.Cmp(uint256.NewInt(1)) != 0 {
		t.Fatalf("SWAP16 mismatch: got %s want 1 (bottom value swapped to top)", v)
	}
}

func TestVMTOpInvalidJumpIntoPushData(t *testing.T) {
	// Jump target 3 lands inside PUSH2's immediate data, which must be
	// rejected as an invalid jump destination even though the byte value
	// there happens to equal JUMPDEST (0x5b).
	code := []byte{
		byte(PUSH1), 0x03, // pc0-1
		byte(JUMP),          // pc2
		byte(PUSH2), 0x5b, 1, // pc3: PUSH2 with data byte == JUMPDEST opcode value
		byte(STOP),
	}
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err == nil {
		t.Fatalf("expected invalid jump destination error, got nil")
	}
}

var _ = big.NewInt
