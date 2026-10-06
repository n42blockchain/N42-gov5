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
	"github.com/n42blockchain/N42/internal/vm/stack"
)

// TestVMTOpClz drives opClz directly (it needs no interpreter access),
// covering the zero-input special case and a nonzero value.
func TestVMTOpClz(t *testing.T) {
	cases := []struct {
		name  string
		value *uint256.Int
		want  uint64
	}{
		{"zero", uint256.NewInt(0), 256},
		{"one", uint256.NewInt(1), 255},
		{"topBitSet", new(uint256.Int).Lsh(uint256.NewInt(1), 255), 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			scope := &ScopeContext{Stack: stack.New(), Memory: NewMemory(), Contract: &Contract{}}
			scope.Stack.Push(c.value)
			pc := uint64(0)
			_, err := opClz(&pc, nil, scope)
			if err != nil {
				t.Fatalf("opClz failed: %v", err)
			}
			got := scope.Stack.Pop()
			if got.Uint64() != c.want {
				t.Fatalf("opClz(%s) = %d, want %d", c.value.Hex(), got.Uint64(), c.want)
			}
		})
	}
}

// TestVMTOpBlobHash covers BLOBHASH for both an in-range index (returns the
// blob hash) and an out-of-range index (returns zero), per EIP-4844.
func TestVMTOpBlobHash(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	// newExecHarnessEVM doesn't seed BlobHashes; set them directly since
	// this test is in-package.
	hash0 := types.Hash{1, 2, 3}
	evm.txContext.BlobHashes = []types.Hash{hash0}

	// In-range: BLOBHASH(0) must equal hash0.
	code := []byte{
		byte(PUSH1), 0x00,
		byte(BLOBHASH),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	ret, _, err := execHarnessCall(t, evm, ibs, code, nil, 100_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	want := new(uint256.Int).SetBytes(hash0[:])
	got := new(uint256.Int).SetBytes(ret)
	if got.Cmp(want) != 0 {
		t.Fatalf("BLOBHASH(0) = %s, want %s", got, want)
	}

	// Out-of-range: BLOBHASH(5) must be zero.
	codeOOR := []byte{
		byte(PUSH1), 0x05,
		byte(BLOBHASH),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	ret2, _, err := execHarnessCall(t, evm, ibs, codeOOR, nil, 100_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if new(uint256.Int).SetBytes(ret2).Sign() != 0 {
		t.Fatalf("BLOBHASH(5) should be zero for an out-of-range index, got %x", ret2)
	}
}

// TestVMTOpBlobBaseFee covers BLOBBASEFEE, including the nil-BlobBaseFee
// fallback-to-zero path.
func TestVMTOpBlobBaseFee(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	code := []byte{
		byte(BLOBBASEFEE),
		byte(PUSH1), 0x00,
		byte(MSTORE),
		byte(PUSH1), 0x20,
		byte(PUSH1), 0x00,
		byte(RETURN),
	}
	ret, _, err := execHarnessCall(t, evm, ibs, code, nil, 100_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	// The harness doesn't set BlobBaseFee, so the nil-fallback path (push 0)
	// must be exercised without panicking.
	if new(uint256.Int).SetBytes(ret).Sign() != 0 {
		t.Fatalf("expected BLOBBASEFEE to be 0 when unset, got %x", ret)
	}
}
