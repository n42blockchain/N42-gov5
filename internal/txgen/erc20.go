// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Minimal hand-assembled ERC20 used by the dev transaction generator to
// exercise the CONTRACT execution path (EVM dispatch, storage writes, logs,
// receipts) under consensus replay — native transfers alone never touch it.
// Supports transfer(address,uint256) and balanceOf(address) with a standard
// Transfer event. Storage layout is simplified (balance slot = the address
// itself, not the Solidity mapping hash) — functionally equivalent for load
// generation, NOT ABI-storage-compatible with real ERC20 tooling.

package txgen

import (
	"encoding/binary"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

// erc20TransferSelector / erc20BalanceOfSelector are the standard ABI selectors.
var (
	erc20TransferSelector  = [4]byte{0xa9, 0x05, 0x9c, 0xbb} // transfer(address,uint256)
	erc20BalanceOfSelector = [4]byte{0x70, 0xa0, 0x82, 0x31} // balanceOf(address)
)

// erc20TransferTopic = keccak256("Transfer(address,address,uint256)").
var erc20TransferTopic = types.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")

// erc20RuntimeCode returns the runtime bytecode. Assembled by hand; the
// label offsets are computed by the builder so they can never drift.
func erc20RuntimeCode() []byte {
	var b []byte
	push1 := func(v byte) { b = append(b, 0x60, v) }
	op := func(codes ...byte) { b = append(b, codes...) }

	// Selector dispatch. Label offsets are backpatched below.
	push1(0x00)
	op(0x35)       // CALLDATALOAD
	push1(0xE0)
	op(0x1c)       // SHR                              [sel]
	op(0x80)       // DUP1
	op(0x63)       // PUSH4 transfer selector
	b = append(b, erc20TransferSelector[:]...)
	op(0x14)       // EQ
	transferJmp := len(b) + 1
	push1(0x00)    // (backpatched) PUSH1 <transfer>
	op(0x57)       // JUMPI
	op(0x80)       // DUP1
	op(0x63)       // PUSH4 balanceOf selector
	b = append(b, erc20BalanceOfSelector[:]...)
	op(0x14)       // EQ
	balanceJmp := len(b) + 1
	push1(0x00)    // (backpatched) PUSH1 <balanceOf>
	op(0x57)       // JUMPI
	push1(0x00)
	op(0x80, 0xfd) // DUP1 REVERT

	// balanceOf(address): return sload(arg0)
	balanceOfAt := len(b)
	op(0x5b)    // JUMPDEST
	push1(0x04)
	op(0x35)    // CALLDATALOAD                        [addr]
	op(0x54)    // SLOAD                               [bal]
	push1(0x00)
	op(0x52)    // MSTORE
	push1(0x20)
	push1(0x00)
	op(0xf3)    // RETURN

	// transfer(to, amount)
	transferAt := len(b)
	op(0x5b)    // JUMPDEST
	push1(0x24)
	op(0x35)       // CALLDATALOAD                     [amt]
	op(0x33, 0x54) // CALLER SLOAD                     [amt, fromBal]
	op(0x81)       // DUP2                             [amt, fromBal, amt]
	op(0x81)       // DUP2                             [amt, fromBal, amt, fromBal]
	op(0x10)       // LT (fromBal < amt)               [amt, fromBal, flag]
	revJmp := len(b) + 1
	push1(0x00)    // (backpatched) PUSH1 <revert>
	op(0x57)       // JUMPI                            [amt, fromBal]
	op(0x81)       // DUP2                             [amt, fromBal, amt]
	op(0x90)       // SWAP1                            [amt, amt, fromBal]
	op(0x03)       // SUB (fromBal - amt)              [amt, newFromBal]
	op(0x33, 0x55) // CALLER SSTORE                    [amt]
	push1(0x04)
	op(0x35)       // CALLDATALOAD                     [amt, to]
	op(0x80, 0x54) // DUP1 SLOAD                       [amt, to, toBal]
	op(0x82)       // DUP3                             [amt, to, toBal, amt]
	op(0x01)       // ADD                              [amt, to, newToBal]
	op(0x90)       // SWAP1                            [amt, newToBal, to]
	op(0x55)       // SSTORE                           [amt]
	push1(0x00)
	op(0x52)       // MSTORE (mem[0..32] = amt)        []
	push1(0x04)
	op(0x35)       // CALLDATALOAD                     [to]
	op(0x33)       // CALLER                           [to, from]
	op(0x7f)       // PUSH32 Transfer topic
	b = append(b, erc20TransferTopic[:]...)
	push1(0x20)
	push1(0x00)    //                                  [to, from, topic, 0x20, 0x00]
	op(0xa3)       // LOG3(off=0, size=32, t1=topic, t2=from, t3=to)
	push1(0x01)
	push1(0x00)
	op(0x52)       // MSTORE (return true)
	push1(0x20)
	push1(0x00)
	op(0xf3)       // RETURN

	// revert
	revertAt := len(b)
	op(0x5b)       // JUMPDEST
	push1(0x00)
	op(0x80, 0xfd) // DUP1 REVERT

	b[transferJmp] = byte(transferAt)
	b[balanceJmp] = byte(balanceOfAt)
	b[revJmp] = byte(revertAt)
	return b
}

// erc20DeployCode returns constructor+runtime: the constructor credits the
// deployer with the full supply (1e27) and returns the runtime code.
func erc20DeployCode() []byte {
	runtime := erc20RuntimeCode()
	supply := uint256.NewInt(0)
	supply.SetAllOne()
	supply = uint256.MustFromDecimal("1000000000000000000000000000") // 1e27

	var ctor []byte
	sb := supply.Bytes() // minimal big-endian
	ctor = append(ctor, 0x5f+byte(len(sb))) // PUSHn supply
	ctor = append(ctor, sb...)
	ctor = append(ctor, 0x33, 0x55) // CALLER SSTORE (key=caller, val=supply)
	// CODECOPY(dest=0, offset=len(ctor), len=len(runtime)); RETURN(0, len)
	// Constructor tail is fixed-length (11 bytes) so its own length is known:
	// PUSH1 len DUP1 PUSH1 off PUSH1 0 CODECOPY PUSH1 0 RETURN
	if len(runtime) > 0xFF {
		panic("erc20 runtime exceeds PUSH1 range")
	}
	off := len(ctor) + 11
	if off > 0xFF {
		panic("erc20 constructor exceeds PUSH1 range")
	}
	ctor = append(ctor,
		0x60, byte(len(runtime)), // PUSH1 runtimeLen
		0x80,                     // DUP1
		0x60, byte(off),          // PUSH1 runtimeOffset
		0x60, 0x00,               // PUSH1 0
		0x39,                     // CODECOPY
		0x60, 0x00,               // PUSH1 0
		0xf3,                     // RETURN
	)
	return append(ctor, runtime...)
}

// erc20TransferCalldata builds transfer(to, amount) calldata.
func erc20TransferCalldata(to types.Address, amount *uint256.Int) []byte {
	data := make([]byte, 4+32+32)
	copy(data[0:4], erc20TransferSelector[:])
	copy(data[4+12:4+32], to[:])
	amount.WriteToSlice(data[4+32 : 4+64])
	return data
}

// erc20BalanceOfCalldata builds balanceOf(addr) calldata.
func erc20BalanceOfCalldata(addr types.Address) []byte {
	data := make([]byte, 4+32)
	copy(data[0:4], erc20BalanceOfSelector[:])
	copy(data[4+12:4+32], addr[:])
	return data
}

// uint64FromBE is a tiny helper for tests.
func uint64FromBE(b []byte) uint64 {
	if len(b) < 8 {
		var p [8]byte
		copy(p[8-len(b):], b)
		return binary.BigEndian.Uint64(p[:])
	}
	return binary.BigEndian.Uint64(b[len(b)-8:])
}
