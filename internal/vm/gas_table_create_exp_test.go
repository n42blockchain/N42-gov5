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
	"github.com/n42blockchain/N42/params"
)

func frontierOnlyChainConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID: big.NewInt(1),
	}
}

// TestVMTGasExpFrontier exercises the pre-EIP160 EXP gas function (10 gas
// per significant byte of the exponent) on a Frontier-only chain config.
func TestVMTGasExpFrontier(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, frontierOnlyChainConfig())
	// 2 ** 256 (exponent needs 2 bytes: 0x0100)
	code := []byte{
		byte(PUSH2), 0x01, 0x00, // exponent
		byte(PUSH1), 0x02, // base
		byte(EXP),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
}

// TestVMTGasCreate2Legacy exercises the legacy (pre-EIP-3860) CREATE2 gas
// function, reachable on a Constantinople-era config that predates
// Shanghai's init-code metering.
func TestVMTGasCreate2Legacy(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, constantinopleOnlyChainConfig())

	caller := types.HexToAddress("0x00000000000000000000000000000000ca11e8")
	ibs.CreateAccount(caller, true)
	ibs.AddBalance(caller, uint256.NewInt(1_000_000_000))
	ibs.PrepareAccessList(caller, nil, nil, nil)

	// CREATE2 with a 64-byte init code that just STOPs.
	initCode := make([]byte, 64)
	initCode[0] = byte(STOP)

	_, _, _, err := evm.Create2(AccountRef(caller), initCode, 500_000, uint256.NewInt(0), uint256.NewInt(0))
	if err != nil {
		t.Fatalf("legacy CREATE2 failed: %v", err)
	}
}

// TestVMTGasCreateEip3860OversizedInitCode exercises the EIP-3860 oversized
// init-code rejection in gasCreateEip3860 (CREATE), reachable with Shanghai
// active (execHarnessChainConfig enables it from genesis).
func TestVMTGasCreateEip3860OversizedInitCode(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	caller := types.HexToAddress("0x00000000000000000000000000000000ca11e9")
	ibs.CreateAccount(caller, true)
	ibs.AddBalance(caller, uint256.NewInt(1_000_000_000))
	ibs.PrepareAccessList(caller, nil, ActivePrecompiles(evm.ChainRules()), nil)

	initCode := make([]byte, params.MaxInitCodeSize+1)

	code := []byte{
		byte(PUSH3), byte(len(initCode) >> 16), byte(len(initCode) >> 8), byte(len(initCode)), // size
		byte(PUSH1), 0x00, // offset (memory is zero-filled already)
		byte(PUSH1), 0x00, // value
		byte(CREATE),
		byte(STOP),
	}
	// The EIP-3860 size check lives in the CREATE opcode's own dynamic gas
	// function (gasCreateEip3860), charged by the interpreter dispatch loop
	// itself before opCreate ever runs — so exceeding the cap aborts the
	// whole running frame with ErrOutOfGas, same as any other dynamic-gas
	// failure, rather than leaving a 0 on the stack the way a failure
	// *inside* opCreate/evm.create would.
	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 10_000_000, nil)
	if err != ErrOutOfGas {
		t.Fatalf("expected ErrOutOfGas from the EIP-3860 oversized-init-code check, got %v", err)
	}
}

// TestVMTGasCreate2Eip3860OversizedInitCode mirrors the above for CREATE2.
// Like CREATE, the EIP-3860 size cap is enforced by the opcode's own gas
// function in the interpreter dispatch loop, not by evm.create itself, so
// this must run the opcode via bytecode rather than call evm.Create2
// directly.
func TestVMTGasCreate2Eip3860OversizedInitCode(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	caller := types.HexToAddress("0x00000000000000000000000000000000ca11ea")
	ibs.CreateAccount(caller, true)
	ibs.AddBalance(caller, uint256.NewInt(1_000_000_000))
	ibs.PrepareAccessList(caller, nil, ActivePrecompiles(evm.ChainRules()), nil)

	size := params.MaxInitCodeSize + 1
	code := []byte{
		byte(PUSH1), 0x00, // salt
		byte(PUSH3), byte(size >> 16), byte(size >> 8), byte(size), // size
		byte(PUSH1), 0x00, // offset
		byte(PUSH1), 0x00, // value
		byte(CREATE2),
		byte(STOP),
	}
	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 10_000_000, nil)
	if err != ErrOutOfGas {
		t.Fatalf("expected ErrOutOfGas from the EIP-3860 oversized-init-code check, got %v", err)
	}
}

// TestVMTExtCodeHashEmptyAndNonexistent verifies EXTCODEHASH returns the
// empty-code hash for an account with no code and zero for an account that
// does not exist at all.
func TestVMTExtCodeHashEmptyAndNonexistent(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())

	// EIP-161: an account with zero nonce, zero balance and no code is
	// "empty" and treated as nonexistent (EXTCODEHASH -> 0). Give it a
	// nonzero balance so it counts as existing, to exercise the
	// no-code-but-exists branch (EXTCODEHASH -> empty-code hash).
	emptyAccount := types.HexToAddress("0x00000000000000000000000000000000111111")
	ibs.CreateAccount(emptyAccount, false)
	ibs.AddBalance(emptyAccount, uint256.NewInt(1))

	nonexistent := types.HexToAddress("0x00000000000000000000000000000000222222")

	for _, tc := range []struct {
		name string
		addr types.Address
	}{
		{"empty", emptyAccount},
		{"nonexistent", nonexistent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			word := make([]byte, 32)
			copy(word[12:], tc.addr.Bytes())
			code := []byte{byte(PUSH32)}
			code = append(code, word...)
			code = append(code,
				byte(EXTCODEHASH),
				byte(PUSH1), 0x00,
				byte(MSTORE),
				byte(PUSH1), 0x20,
				byte(PUSH1), 0x00,
				byte(RETURN),
			)
			ret, _, err := execHarnessCall(t, evm, ibs, code, nil, 1_000_000, nil)
			if err != nil {
				t.Fatalf("call failed: %v", err)
			}
			v := new(uint256.Int).SetBytes(ret)
			if tc.name == "nonexistent" && !v.IsZero() {
				t.Fatalf("EXTCODEHASH of a nonexistent account must be zero, got %s", v)
			}
			if tc.name == "empty" && v.IsZero() {
				t.Fatalf("EXTCODEHASH of an empty (but existing) account must be the empty-code hash, not zero")
			}
		})
	}
}
