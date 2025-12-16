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
	"math"
	"testing"

	"github.com/holiman/uint256"
)

// =============================================================================
// Gas Calculation Tests
// =============================================================================

func TestGasConstants(t *testing.T) {
	// Verify gas constants are correctly defined
	if GasQuickStep != 2 {
		t.Errorf("GasQuickStep should be 2, got %d", GasQuickStep)
	}
	if GasFastestStep != 3 {
		t.Errorf("GasFastestStep should be 3, got %d", GasFastestStep)
	}
	if GasFastStep != 5 {
		t.Errorf("GasFastStep should be 5, got %d", GasFastStep)
	}
	if GasMidStep != 8 {
		t.Errorf("GasMidStep should be 8, got %d", GasMidStep)
	}
	if GasSlowStep != 10 {
		t.Errorf("GasSlowStep should be 10, got %d", GasSlowStep)
	}
	if GasExtStep != 20 {
		t.Errorf("GasExtStep should be 20, got %d", GasExtStep)
	}
	t.Logf("✓ Gas constants are correct")
}

func TestSafeMulExtended(t *testing.T) {
	tests := []struct {
		name     string
		a, b     uint64
		expected uint64
		overflow bool
	}{
		{"zero_a", 0, 100, 0, false},
		{"zero_b", 100, 0, 0, false},
		{"both_zero", 0, 0, 0, false},
		{"normal", 10, 20, 200, false},
		{"large_no_overflow", 1000000, 1000000, 1000000000000, false},
		{"overflow", math.MaxUint64, 2, 0, true}, // overflow case, expected value doesn't matter
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, overflow := safeMul(tt.a, tt.b)
			if overflow != tt.overflow {
				t.Errorf("safeMul(%d, %d) overflow = %v, want %v", tt.a, tt.b, overflow, tt.overflow)
			}
			if !overflow && result != tt.expected {
				t.Errorf("safeMul(%d, %d) = %d, want %d", tt.a, tt.b, result, tt.expected)
			}
		})
	}
	t.Logf("✓ safeMul works correctly")
}

func TestSafeAddExtended(t *testing.T) {
	tests := []struct {
		name     string
		a, b     uint64
		expected uint64
		overflow bool
	}{
		{"zero", 0, 0, 0, false},
		{"normal", 10, 20, 30, false},
		{"large_no_overflow", math.MaxUint64 - 10, 5, math.MaxUint64 - 5, false},
		{"overflow", math.MaxUint64, 1, 0, true},
		{"overflow_large", math.MaxUint64, math.MaxUint64, 0, true}, // overflow, expected doesn't matter
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, overflow := safeAdd(tt.a, tt.b)
			if overflow != tt.overflow {
				t.Errorf("safeAdd(%d, %d) overflow = %v, want %v", tt.a, tt.b, overflow, tt.overflow)
			}
			if !overflow && result != tt.expected {
				t.Errorf("safeAdd(%d, %d) = %d, want %d", tt.a, tt.b, result, tt.expected)
			}
		})
	}
	t.Logf("✓ safeAdd works correctly")
}

func TestToWordSizeExtended(t *testing.T) {
	tests := []struct {
		name     string
		size     uint64
		expected uint64
	}{
		{"zero", 0, 0},
		{"one_byte", 1, 1},
		{"32_bytes", 32, 1},
		{"33_bytes", 33, 2},
		{"64_bytes", 64, 2},
		{"65_bytes", 65, 3},
		{"large", 1000, 32},
		{"near_max", math.MaxUint64 - 30, math.MaxUint64/32 + 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := toWordSize(tt.size)
			if result != tt.expected {
				t.Errorf("toWordSize(%d) = %d, want %d", tt.size, result, tt.expected)
			}
		})
	}
	t.Logf("✓ toWordSize works correctly")
}

func TestToWordSizePublic(t *testing.T) {
	tests := []struct {
		name     string
		size     uint64
		expected uint64
	}{
		{"zero", 0, 0},
		{"one_byte", 1, 1},
		{"32_bytes", 32, 1},
		{"33_bytes", 33, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ToWordSize(tt.size)
			if result != tt.expected {
				t.Errorf("ToWordSize(%d) = %d, want %d", tt.size, result, tt.expected)
			}
		})
	}
	t.Logf("✓ ToWordSize works correctly")
}

func TestCallGas(t *testing.T) {
	tests := []struct {
		name         string
		isEip150     bool
		availableGas uint64
		base         uint64
		callCost     *uint256.Int
		expectedGas  uint64
		expectError  bool
	}{
		{
			name:         "eip150_large_cost",
			isEip150:     true,
			availableGas: 1000,
			base:         100,
			callCost:     uint256.NewInt(10000),
			expectedGas:  886, // (1000-100) - (1000-100)/64 = 900 - 14 = 886
			expectError:  false,
		},
		{
			name:         "eip150_small_cost",
			isEip150:     true,
			availableGas: 1000,
			base:         100,
			callCost:     uint256.NewInt(100),
			expectedGas:  100,
			expectError:  false,
		},
		{
			name:         "pre_eip150",
			isEip150:     false,
			availableGas: 1000,
			base:         100,
			callCost:     uint256.NewInt(500),
			expectedGas:  500,
			expectError:  false,
		},
		{
			name:         "pre_eip150_overflow",
			isEip150:     false,
			availableGas: 1000,
			base:         100,
			callCost:     new(uint256.Int).SetAllOne(),
			expectedGas:  0,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gas, err := callGas(tt.isEip150, tt.availableGas, tt.base, tt.callCost)
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if gas != tt.expectedGas {
					t.Errorf("callGas() = %d, want %d", gas, tt.expectedGas)
				}
			}
		})
	}
	t.Logf("✓ callGas works correctly")
}

// =============================================================================
// Memory Calculation Tests
// =============================================================================

func TestCalcMemSize64(t *testing.T) {
	tests := []struct {
		name     string
		off      *uint256.Int
		l        *uint256.Int
		expected uint64
		overflow bool
	}{
		{"zero_length", uint256.NewInt(100), uint256.NewInt(0), 0, false},
		{"normal", uint256.NewInt(10), uint256.NewInt(20), 30, false},
		{"large_offset_zero_length", new(uint256.Int).SetAllOne(), uint256.NewInt(0), 0, false},
		{"length_not_uint64", uint256.NewInt(0), new(uint256.Int).SetAllOne(), 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, overflow := calcMemSize64(tt.off, tt.l)
			if overflow != tt.overflow {
				t.Errorf("calcMemSize64 overflow = %v, want %v", overflow, tt.overflow)
			}
			if !overflow && result != tt.expected {
				t.Errorf("calcMemSize64 = %d, want %d", result, tt.expected)
			}
		})
	}
	t.Logf("✓ calcMemSize64 works correctly")
}

func TestCalcMemSize64WithUint(t *testing.T) {
	tests := []struct {
		name     string
		off      *uint256.Int
		length64 uint64
		expected uint64
		overflow bool
	}{
		{"zero_length", uint256.NewInt(100), 0, 0, false},
		{"normal", uint256.NewInt(10), 20, 30, false},
		{"overflow_offset", new(uint256.Int).SetAllOne(), 1, 0, true},
		{"overflow_sum", uint256.NewInt(math.MaxUint64), 1, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, overflow := calcMemSize64WithUint(tt.off, tt.length64)
			if overflow != tt.overflow {
				t.Errorf("calcMemSize64WithUint overflow = %v, want %v", overflow, tt.overflow)
			}
			if !overflow && result != tt.expected {
				t.Errorf("calcMemSize64WithUint = %d, want %d", result, tt.expected)
			}
		})
	}
	t.Logf("✓ calcMemSize64WithUint works correctly")
}

// =============================================================================
// Data Handling Tests
// =============================================================================

func TestGetData(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03, 0x04, 0x05}

	tests := []struct {
		name     string
		start    uint64
		size     uint64
		expected []byte
	}{
		{"full", 0, 5, []byte{0x01, 0x02, 0x03, 0x04, 0x05}},
		{"partial_start", 0, 3, []byte{0x01, 0x02, 0x03}},
		{"partial_middle", 2, 2, []byte{0x03, 0x04}},
		{"with_padding", 3, 5, []byte{0x04, 0x05, 0x00, 0x00, 0x00}},
		{"start_beyond", 10, 3, []byte{0x00, 0x00, 0x00}},
		{"zero_size", 0, 0, []byte{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getData(data, tt.start, tt.size)
			if len(result) != len(tt.expected) {
				t.Errorf("getData length = %d, want %d", len(result), len(tt.expected))
			}
			for i := range result {
				if result[i] != tt.expected[i] {
					t.Errorf("getData[%d] = %x, want %x", i, result[i], tt.expected[i])
				}
			}
		})
	}
	t.Logf("✓ getData works correctly")
}

func TestGetDataBig(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03, 0x04, 0x05}

	tests := []struct {
		name     string
		start    *uint256.Int
		size     uint64
		expected []byte
	}{
		{"normal", uint256.NewInt(0), 3, []byte{0x01, 0x02, 0x03}},
		{"overflow_start", new(uint256.Int).SetAllOne(), 3, []byte{0x00, 0x00, 0x00}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getDataBig(data, tt.start, tt.size)
			if len(result) != len(tt.expected) {
				t.Errorf("getDataBig length = %d, want %d", len(result), len(tt.expected))
			}
		})
	}
	t.Logf("✓ getDataBig works correctly")
}

func TestAllZero(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected bool
	}{
		{"empty", []byte{}, true},
		{"all_zeros", []byte{0x00, 0x00, 0x00}, true},
		{"has_nonzero", []byte{0x00, 0x01, 0x00}, false},
		{"single_zero", []byte{0x00}, true},
		{"single_nonzero", []byte{0x01}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := allZero(tt.data)
			if result != tt.expected {
				t.Errorf("allZero(%v) = %v, want %v", tt.data, result, tt.expected)
			}
		})
	}
	t.Logf("✓ allZero works correctly")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkSafeMul(b *testing.B) {
	a := uint64(12345678)
	bVal := uint64(87654321)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		safeMul(a, bVal)
	}
}

func BenchmarkSafeAdd(b *testing.B) {
	a := uint64(12345678)
	bVal := uint64(87654321)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		safeAdd(a, bVal)
	}
}

func BenchmarkToWordSize(b *testing.B) {
	size := uint64(1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		toWordSize(size)
	}
}

func BenchmarkCalcMemSize64(b *testing.B) {
	off := uint256.NewInt(100)
	l := uint256.NewInt(200)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		calcMemSize64(off, l)
	}
}

func BenchmarkGetData(b *testing.B) {
	data := make([]byte, 1024)
	for i := range data {
		data[i] = byte(i % 256)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		getData(data, 100, 200)
	}
}

func BenchmarkAllZero(b *testing.B) {
	data := make([]byte, 32)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		allZero(data)
	}
}

func BenchmarkCallGasEIP150(b *testing.B) {
	callCost := uint256.NewInt(1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		callGas(true, 10000, 100, callCost)
	}
}
