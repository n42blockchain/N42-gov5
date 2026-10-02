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
)

func TestMemoryGasCostZeroSize(t *testing.T) {
	mem := NewMemory()
	got, err := memoryGasCost(mem, 0)
	if err != nil {
		t.Fatalf("memoryGasCost(0) error: %v", err)
	}
	if got != 0 {
		t.Errorf("memoryGasCost(0) = %d, want 0", got)
	}
}

func TestMemoryGasCostOverflowRejected(t *testing.T) {
	mem := NewMemory()
	_, err := memoryGasCost(mem, 0x1FFFFFFFE0+1)
	if err != ErrGasUintOverflow {
		t.Fatalf("err = %v, want %v", err, ErrGasUintOverflow)
	}
}

func TestMemoryGasCostGrowthIsMonotonicAndQuadratic(t *testing.T) {
	mem := NewMemory()
	// Growing memory from 0 to 32 bytes (1 word).
	cost32, err := memoryGasCost(mem, 32)
	if err != nil {
		t.Fatalf("memoryGasCost(32) error: %v", err)
	}
	if cost32 == 0 {
		t.Errorf("memoryGasCost(32) = 0, want > 0")
	}

	// memoryGasCost tracks the last-charged total fee on the Memory itself
	// (mem.lastGasCost), independent of whether the memory was physically
	// resized; re-requesting the same size must therefore charge nothing
	// further.
	costAgain, err := memoryGasCost(mem, 32)
	if err != nil {
		t.Fatalf("memoryGasCost(32) second call error: %v", err)
	}
	if costAgain != 0 {
		t.Errorf("re-requesting the same memory size cost %d gas, want 0 (no further growth)", costAgain)
	}

	// Growing further to 10 words (320 bytes) must cost more than the
	// linear-only extrapolation because of the quadratic component.
	cost320, err := memoryGasCost(mem, 320)
	if err != nil {
		t.Fatalf("memoryGasCost(320) error: %v", err)
	}
	if cost320 <= cost32 {
		t.Errorf("memoryGasCost(320) = %d, want > memoryGasCost(32) = %d", cost320, cost32)
	}
}

func TestCallGasPre150ReturnsRequestedCost(t *testing.T) {
	requested := uint256.NewInt(5000)
	got, err := callGas(false, 1_000_000, 0, requested)
	if err != nil {
		t.Fatalf("callGas error: %v", err)
	}
	if got != 5000 {
		t.Errorf("callGas(pre-150) = %d, want 5000", got)
	}
}

func TestCallGasEIP150CapsAt63Over64(t *testing.T) {
	// Request far more gas than the 63/64 rule allows; the result must be
	// capped at floor(available * 63/64), not the requested amount.
	availableGas := uint64(64_000)
	base := uint64(0)
	requested := uint256.NewInt(1_000_000) // way more than available allows
	got, err := callGas(true, availableGas, base, requested)
	if err != nil {
		t.Fatalf("callGas error: %v", err)
	}
	want := availableGas - availableGas/64
	if got != want {
		t.Errorf("callGas(EIP150, capped) = %d, want %d", got, want)
	}
	if got >= requested.Uint64() {
		t.Errorf("callGas(EIP150) = %d, should be less than the requested %d", got, requested.Uint64())
	}
}

func TestCallGasEIP150GrantsRequestedWhenUnderCap(t *testing.T) {
	// When the requested amount is comfortably below the 63/64 cap, the
	// exact requested amount must be forwarded.
	availableGas := uint64(1_000_000)
	base := uint64(0)
	requested := uint256.NewInt(1000)
	got, err := callGas(true, availableGas, base, requested)
	if err != nil {
		t.Fatalf("callGas error: %v", err)
	}
	if got != 1000 {
		t.Errorf("callGas(EIP150, under cap) = %d, want 1000", got)
	}
}

func TestCallGasEIP150SubtractsBaseFirst(t *testing.T) {
	// The 63/64 split applies to (availableGas - base), not availableGas
	// itself.
	availableGas := uint64(1000)
	base := uint64(100)
	requested := uint256.NewInt(10_000) // force the cap to bind
	got, err := callGas(true, availableGas, base, requested)
	if err != nil {
		t.Fatalf("callGas error: %v", err)
	}
	remaining := availableGas - base
	want := remaining - remaining/64
	if got != want {
		t.Errorf("callGas(EIP150, base subtracted) = %d, want %d", got, want)
	}
}
