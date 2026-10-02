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
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// TestEVMMaxCallDepthEnforced builds a contract that recursively CALLs
// itself, incrementing a storage counter each invocation, and checks that
// recursion stops exactly at params.CallCreateDepth (the deepest frame's
// CALL fails with ErrDepth, which the CALL opcode swallows as a 0 success
// flag rather than propagating).
func TestEVMMaxCallDepthEnforced(t *testing.T) {
	self := types.HexToAddress("0x5e1f00000000000000000000000000005e1f00")

	// Program: counter = SLOAD(0) + 1; SSTORE(0, counter); CALL(self, gas=all
	// forwarded via the 63/64 rule, no value, no args, no ret); STOP.
	code := []byte{
		byte(PUSH1), 0x01,
		byte(PUSH1), 0x00,
		byte(SLOAD),
		byte(ADD),
		byte(PUSH1), 0x00,
		byte(SSTORE),

		// CALL(gas, addr, value=0, argsOffset=0, argsSize=0, retOffset=0, retSize=0)
		byte(PUSH1), 0x00, // retSize
		byte(PUSH1), 0x00, // retOffset
		byte(PUSH1), 0x00, // argsSize
		byte(PUSH1), 0x00, // argsOffset
		byte(PUSH1), 0x00, // value
		byte(PUSH20),
	}
	code = append(code, self.Bytes()...)
	code = append(code, byte(GAS), byte(CALL), byte(STOP))

	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	ibs.CreateAccount(self, true)
	ibs.SetCode(self, code)

	caller := types.HexToAddress("0xcacacacacacacacacacacacacacacacacacacac")
	ibs.CreateAccount(caller, true)
	ibs.AddBalance(caller, uint256.NewInt(0))
	ibs.PrepareAccessList(caller, &self, nil, nil)

	// A very large gas budget is required: EIP-150 forwards at most 63/64
	// of the available gas per CALL, so after ~1024 nested calls only a
	// tiny fraction of the original gas remains. 1e13 leaves roughly 1e6
	// gas at the deepest frame, comfortably more than one more
	// SLOAD+ADD+SSTORE+CALL sequence costs.
	const startGas = 10_000_000_000_000

	_, _, err := evm.Call(AccountRef(caller), self, nil, startGas, uint256.NewInt(0), false)
	if err != nil {
		t.Fatalf("top-level call failed: %v", err)
	}

	var zeroSlot types.Hash
	var counter uint256.Int
	ibs.GetState(self, &zeroSlot, &counter)
	depth := counter.Uint64()

	// The recursion must stop at exactly CallCreateDepth+1 invocations: the
	// top-level call (depth 0) plus CallCreateDepth nested CALLs, the last
	// of which is rejected by the depth check before it can execute (so it
	// never increments the counter).
	want := params.CallCreateDepth + 1
	if depth != want {
		t.Errorf("recursion count = %d, want %d (max call depth not enforced correctly)", depth, want)
	}
}
