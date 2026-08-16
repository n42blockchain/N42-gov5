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

// Tests for the Amsterdam (Glamsterdam EL) scheduled EIPs: 8024 legacy
// stack ops, 2780/7976/7981/8038 intrinsic-gas composition, 8037 state-gas
// constants, and 7954 size limits.

package vm

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/params"
)

// --- EIP-8024: immediate decoding ---

func TestEIP8024DupNSwapNDecoding(t *testing.T) {
	// decode_single(x) = (x+145) % 256; valid 0..90 and 128..255 -> n in 17..235.
	cases := []struct {
		imm  byte
		n    int
		ok   bool
	}{
		{0, 145, true},
		{90, 235, true},
		{91, 0, false},
		{127, 0, false},
		{128, 17, true},
		{255, 144, true},
	}
	for _, c := range cases {
		code := []byte{byte(DUPN), c.imm}
		scope := createTestScope(code)
		// Fill the stack deep enough for any valid depth.
		for i := 0; i < 236; i++ {
			scope.Stack.Push(uint256.NewInt(uint64(i)))
		}
		pc := uint64(0)
		_, err := opDupN(&pc, nil, scope)
		if c.ok {
			if err != nil {
				t.Fatalf("imm=%d: unexpected error %v", c.imm, err)
			}
			// Stack had 236 items (top=235 pushed last, value i at depth 236-i).
			// Dup(n) copies the n'th from top = value 236-n.
			top := scope.Stack.Pop()
			if top.Uint64() != uint64(236-c.n) {
				t.Fatalf("imm=%d: dup depth wrong: got %d want %d (n=%d)", c.imm, top.Uint64(), 236-c.n, c.n)
			}
			if pc != 1 {
				t.Fatalf("imm=%d: pc=%d want 1 (immediate skipped)", c.imm, pc)
			}
		} else if err == nil {
			t.Fatalf("imm=%d: expected exceptional halt", c.imm)
		}
	}
}

func TestEIP8024ExchangeDecoding(t *testing.T) {
	// decode_pair: k = x ^ 143; q,r = divmod(k,16); (q+1,r+1) if q<r else (r+1,29-q).
	// x=143 -> k=0 -> q=0,r=0 -> q>=r -> (1,29).
	code := []byte{byte(EXCHANGE), 143}
	scope := createTestScope(code)
	for i := 0; i < 31; i++ {
		scope.Stack.Push(uint256.NewInt(uint64(i)))
	}
	// Stack holds 0..30, top=30; depth d (1-based) holds 31-d. (n,m)=(1,29)
	// swaps the 2nd item (29) with the 30th item (1).
	pc := uint64(0)
	if _, err := opExchange(&pc, nil, scope); err != nil {
		t.Fatalf("EXCHANGE failed: %v", err)
	}
	if got := scope.Stack.Back(1).Uint64(); got != 1 {
		t.Fatalf("EXCHANGE n slot: got %d want 1", got)
	}
	if got := scope.Stack.Back(29).Uint64(); got != 29 {
		t.Fatalf("EXCHANGE m slot: got %d want 29", got)
	}
	// Invalid immediate range 82..127 halts.
	badCode := []byte{byte(EXCHANGE), 100}
	badScope := createTestScope(badCode)
	for i := 0; i < 31; i++ {
		badScope.Stack.Push(uint256.NewInt(uint64(i)))
	}
	pc = 0
	if _, err := opExchange(&pc, nil, badScope); err == nil {
		t.Fatal("EXCHANGE imm=100: expected exceptional halt")
	}
}

func TestEIP8024SwapN(t *testing.T) {
	// SWAPN imm=128 -> n=17: swap the 18th item with the top.
	code := []byte{byte(SWAPN), 128}
	scope := createTestScope(code)
	for i := 0; i < 20; i++ {
		scope.Stack.Push(uint256.NewInt(uint64(i)))
	}
	// top=19; 18th from top holds 20-18=2.
	pc := uint64(0)
	if _, err := opSwapN(&pc, nil, scope); err != nil {
		t.Fatalf("SWAPN failed: %v", err)
	}
	if got := scope.Stack.Peek().Uint64(); got != 2 {
		t.Fatalf("SWAPN top: got %d want 2", got)
	}
	if got := scope.Stack.Back(17).Uint64(); got != 19 {
		t.Fatalf("SWAPN depth-18: got %d want 19", got)
	}
}

// --- Amsterdam jump table wiring ---

func TestAmsterdamJumpTableHas8024(t *testing.T) {
	jt := newGlamsterdamInstructionSet()
	for _, op := range []OpCode{DUPN, SWAPN, EXCHANGE} {
		if jt[op] == nil || jt[op].execute == nil {
			t.Fatalf("opcode %s missing from Glamsterdam jump table", op)
		}
		if jt[op].constantGas != GasFastestStep {
			t.Fatalf("opcode %s gas = %d, want %d", op, jt[op].constantGas, GasFastestStep)
		}
	}
	if jt[SLOTNUM] == nil || jt[SLOTNUM].execute == nil {
		t.Fatal("SLOTNUM missing from Glamsterdam jump table (inherited from Fusaka)")
	}
}

// --- EIP-7976: calldata floor ---

func TestEIP7976FloorDataGas(t *testing.T) {
	data := []byte{0, 0, 1, 2} // 2 zero + 2 nonzero -> tokens = 2 + 2*4 = 10
	pre := FloorDataGas(data)
	if pre != 21000+10*TotalCostFloorPerToken {
		t.Fatalf("pre-Amsterdam floor = %d", pre)
	}
	post := FloorDataGas(data, true)
	want := 21000 + 10*params.TotalCostFloorPerTokenEIP7976
	if post != want {
		t.Fatalf("Amsterdam floor = %d, want %d", post, want)
	}
}

// --- Constants sanity (2780 / 8037 / 8038 / 7954 / 7981) ---

func TestAmsterdamConstants(t *testing.T) {
	// EIP-2780: plain transfer still totals 21000.
	if params.TxBaseCostEIP2780+params.TxColdAccountEIP2780+params.TxValueCostEIP2780 != 21000 {
		t.Fatal("EIP-2780 components must sum to 21000 for a plain transfer")
	}
	// EIP-8037 headline state-gas figures.
	if params.StateBytesPerNewAccount*params.CostPerStateByteEIP8037 != 183600 {
		t.Fatal("new-account state gas != 183600")
	}
	if params.StateBytesPerStorageSet*params.CostPerStateByteEIP8037 != 97920 {
		t.Fatal("fresh-slot state gas != 97920")
	}
	// EIP-7981 surcharges over the EIP-8038 base prices.
	if params.TxAccessListAddressGasEIP8038+params.AccessListAddressBytes*params.AccessListBytesSurchargeEIP7981 != 2900+1280 {
		t.Fatal("AL address cost mismatch")
	}
	if params.TxAccessListStorageKeyGasEIP8038+params.AccessListStorageKeyBytes*params.AccessListBytesSurchargeEIP7981 != 2000+2048 {
		t.Fatal("AL storage-key cost mismatch")
	}
	// EIP-7954.
	if params.MaxCodeSizeEIP7954 != 65536 || params.MaxInitCodeSizeEIP7954 != 131072 {
		t.Fatal("EIP-7954 size limits wrong")
	}
}
