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

func TestSafeUint64ToInt64(t *testing.T) {
	tests := []struct {
		name     string
		input    uint64
		expected int64
		ok       bool
	}{
		{"zero", 0, 0, true},
		{"one", 1, 1, true},
		{"max_int64", math.MaxInt64, math.MaxInt64, true},
		{"max_int64_plus_1", math.MaxInt64 + 1, 0, false},
		{"max_uint64", math.MaxUint64, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := SafeUint64ToInt64(tt.input)
			if ok != tt.ok {
				t.Errorf("SafeUint64ToInt64(%d) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if ok && result != tt.expected {
				t.Errorf("SafeUint64ToInt64(%d) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSafeUint64ToInt(t *testing.T) {
	tests := []struct {
		name     string
		input    uint64
		expected int
		ok       bool
	}{
		{"zero", 0, 0, true},
		{"one", 1, 1, true},
		{"max_int", uint64(math.MaxInt), math.MaxInt, true},
		{"max_int_plus_1", uint64(math.MaxInt) + 1, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := SafeUint64ToInt(tt.input)
			if ok != tt.ok {
				t.Errorf("SafeUint64ToInt(%d) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if ok && result != tt.expected {
				t.Errorf("SafeUint64ToInt(%d) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSafeUint64ToUint32(t *testing.T) {
	tests := []struct {
		name     string
		input    uint64
		expected uint32
		ok       bool
	}{
		{"zero", 0, 0, true},
		{"one", 1, 1, true},
		{"max_uint32", math.MaxUint32, math.MaxUint32, true},
		{"max_uint32_plus_1", math.MaxUint32 + 1, 0, false},
		{"max_uint64", math.MaxUint64, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := SafeUint64ToUint32(tt.input)
			if ok != tt.ok {
				t.Errorf("SafeUint64ToUint32(%d) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if ok && result != tt.expected {
				t.Errorf("SafeUint64ToUint32(%d) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSafeInt64ToInt(t *testing.T) {
	tests := []struct {
		name     string
		input    int64
		expected int
		ok       bool
	}{
		{"zero", 0, 0, true},
		{"one", 1, 1, true},
		{"negative_one", -1, -1, true},
		{"max_int", int64(math.MaxInt), math.MaxInt, true},
		{"min_int", int64(math.MinInt), math.MinInt, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := SafeInt64ToInt(tt.input)
			if ok != tt.ok {
				t.Errorf("SafeInt64ToInt(%d) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if ok && result != tt.expected {
				t.Errorf("SafeInt64ToInt(%d) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSafeUint256ToInt64(t *testing.T) {
	tests := []struct {
		name     string
		input    *uint256.Int
		expected int64
		ok       bool
	}{
		{"zero", uint256.NewInt(0), 0, true},
		{"one", uint256.NewInt(1), 1, true},
		{"max_int64", uint256.NewInt(uint64(math.MaxInt64)), math.MaxInt64, true},
		{"max_int64_plus_1", uint256.NewInt(uint64(math.MaxInt64) + 1), 0, false},
		{"large_uint256", new(uint256.Int).Lsh(uint256.NewInt(1), 128), 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := SafeUint256ToInt64(tt.input)
			if ok != tt.ok {
				t.Errorf("SafeUint256ToInt64 ok = %v, want %v", ok, tt.ok)
			}
			if ok && result != tt.expected {
				t.Errorf("SafeUint256ToInt64 = %d, want %d", result, tt.expected)
			}
		})
	}
}

func TestSafeUint256ToUint64(t *testing.T) {
	tests := []struct {
		name     string
		input    *uint256.Int
		expected uint64
		ok       bool
	}{
		{"zero", uint256.NewInt(0), 0, true},
		{"one", uint256.NewInt(1), 1, true},
		{"max_uint64", uint256.NewInt(math.MaxUint64), math.MaxUint64, true},
		{"large_uint256", new(uint256.Int).Lsh(uint256.NewInt(1), 128), 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := SafeUint256ToUint64(tt.input)
			if ok != tt.ok {
				t.Errorf("SafeUint256ToUint64 ok = %v, want %v", ok, tt.ok)
			}
			if ok && result != tt.expected {
				t.Errorf("SafeUint256ToUint64 = %d, want %d", result, tt.expected)
			}
		})
	}
}

func TestMustSafeUint64ToInt64(t *testing.T) {
	tests := []struct {
		name     string
		input    uint64
		expected int64
	}{
		{"zero", 0, 0},
		{"one", 1, 1},
		{"max_int64", math.MaxInt64, math.MaxInt64},
		{"max_int64_plus_1", math.MaxInt64 + 1, math.MaxInt64},
		{"max_uint64", math.MaxUint64, math.MaxInt64},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if result := MustSafeUint64ToInt64(tt.input); result != tt.expected {
				t.Errorf("MustSafeUint64ToInt64(%d) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMustSafeUint64ToInt(t *testing.T) {
	tests := []struct {
		name     string
		input    uint64
		expected int
	}{
		{"zero", 0, 0},
		{"one", 1, 1},
		{"max_int", uint64(math.MaxInt), math.MaxInt},
		{"max_int_plus_1", uint64(math.MaxInt) + 1, math.MaxInt},
		{"max_uint64", math.MaxUint64, math.MaxInt},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if result := MustSafeUint64ToInt(tt.input); result != tt.expected {
				t.Errorf("MustSafeUint64ToInt(%d) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSafeIntToUint64(t *testing.T) {
	tests := []struct {
		name     string
		input    int
		expected uint64
		ok       bool
	}{
		{"zero", 0, 0, true},
		{"positive", 100, 100, true},
		{"negative", -1, 0, false},
		{"negative_large", -100, 0, false},
		{"max_int", math.MaxInt, uint64(math.MaxInt), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := SafeIntToUint64(tt.input)
			if ok != tt.ok {
				t.Errorf("SafeIntToUint64(%d) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if ok && result != tt.expected {
				t.Errorf("SafeIntToUint64(%d) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSafeUint64ToUint8(t *testing.T) {
	tests := []struct {
		name     string
		input    uint64
		expected uint8
		ok       bool
	}{
		{"zero", 0, 0, true},
		{"one", 1, 1, true},
		{"max_uint8", 255, 255, true},
		{"max_uint8_plus_1", 256, 0, false},
		{"large", math.MaxUint64, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := SafeUint64ToUint8(tt.input)
			if ok != tt.ok {
				t.Errorf("SafeUint64ToUint8(%d) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if ok && result != tt.expected {
				t.Errorf("SafeUint64ToUint8(%d) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSafeUint64ToUint16(t *testing.T) {
	tests := []struct {
		name     string
		input    uint64
		expected uint16
		ok       bool
	}{
		{"zero", 0, 0, true},
		{"one", 1, 1, true},
		{"max_uint16", 65535, 65535, true},
		{"max_uint16_plus_1", 65536, 0, false},
		{"large", math.MaxUint64, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := SafeUint64ToUint16(tt.input)
			if ok != tt.ok {
				t.Errorf("SafeUint64ToUint16(%d) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if ok && result != tt.expected {
				t.Errorf("SafeUint64ToUint16(%d) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSafeAddUint64(t *testing.T) {
	tests := []struct {
		name     string
		a, b     uint64
		expected uint64
		ok       bool
	}{
		{"zero_plus_zero", 0, 0, 0, true},
		{"one_plus_one", 1, 1, 2, true},
		{"max_minus_1_plus_1", math.MaxUint64 - 1, 1, math.MaxUint64, true},
		{"max_plus_1_overflow", math.MaxUint64, 1, 0, false},
		{"large_plus_large_overflow", math.MaxUint64, math.MaxUint64, 0, false},
		{"half_plus_half", math.MaxUint64 / 2, math.MaxUint64 / 2, math.MaxUint64 - 1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := SafeAddUint64(tt.a, tt.b)
			if ok != tt.ok {
				t.Errorf("SafeAddUint64(%d, %d) ok = %v, want %v", tt.a, tt.b, ok, tt.ok)
			}
			if ok && result != tt.expected {
				t.Errorf("SafeAddUint64(%d, %d) = %d, want %d", tt.a, tt.b, result, tt.expected)
			}
		})
	}
}

func TestSafeMulUint64(t *testing.T) {
	tests := []struct {
		name     string
		a, b     uint64
		expected uint64
		ok       bool
	}{
		{"zero_mul_zero", 0, 0, 0, true},
		{"one_mul_one", 1, 1, 1, true},
		{"two_mul_three", 2, 3, 6, true},
		{"max_mul_one", math.MaxUint64, 1, math.MaxUint64, true},
		{"one_mul_max", 1, math.MaxUint64, math.MaxUint64, true},
		{"zero_mul_max", 0, math.MaxUint64, 0, true},
		{"large_mul_2_overflow", math.MaxUint64/2 + 1, 2, 0, false},
		{"max_mul_2_overflow", math.MaxUint64, 2, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := SafeMulUint64(tt.a, tt.b)
			if ok != tt.ok {
				t.Errorf("SafeMulUint64(%d, %d) ok = %v, want %v", tt.a, tt.b, ok, tt.ok)
			}
			if ok && result != tt.expected {
				t.Errorf("SafeMulUint64(%d, %d) = %d, want %d", tt.a, tt.b, result, tt.expected)
			}
		})
	}
}

func TestSafeSubUint64(t *testing.T) {
	tests := []struct {
		name     string
		a, b     uint64
		expected uint64
		ok       bool
	}{
		{"zero_minus_zero", 0, 0, 0, true},
		{"one_minus_one", 1, 1, 0, true},
		{"ten_minus_three", 10, 3, 7, true},
		{"zero_minus_one_underflow", 0, 1, 0, false},
		{"five_minus_ten_underflow", 5, 10, 0, false},
		{"max_minus_max", math.MaxUint64, math.MaxUint64, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := SafeSubUint64(tt.a, tt.b)
			if ok != tt.ok {
				t.Errorf("SafeSubUint64(%d, %d) ok = %v, want %v", tt.a, tt.b, ok, tt.ok)
			}
			if ok && result != tt.expected {
				t.Errorf("SafeSubUint64(%d, %d) = %d, want %d", tt.a, tt.b, result, tt.expected)
			}
		})
	}
}

func TestSafeInt64ToUint64(t *testing.T) {
	tests := []struct {
		name     string
		input    int64
		expected uint64
		ok       bool
	}{
		{"zero", 0, 0, true},
		{"positive", 100, 100, true},
		{"max_int64", math.MaxInt64, uint64(math.MaxInt64), true},
		{"negative", -1, 0, false},
		{"min_int64", math.MinInt64, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := SafeInt64ToUint64(tt.input)
			if ok != tt.ok {
				t.Errorf("SafeInt64ToUint64(%d) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if ok && result != tt.expected {
				t.Errorf("SafeInt64ToUint64(%d) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkSafeUint64ToInt64(b *testing.B) {
	for i := 0; i < b.N; i++ {
		SafeUint64ToInt64(uint64(i))
	}
}

func BenchmarkSafeUint256ToInt64(b *testing.B) {
	v := uint256.NewInt(1000000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SafeUint256ToInt64(v)
	}
}

func BenchmarkMustSafeUint64ToInt64(b *testing.B) {
	for i := 0; i < b.N; i++ {
		MustSafeUint64ToInt64(uint64(i))
	}
}

func BenchmarkSafeAddUint64(b *testing.B) {
	for i := 0; i < b.N; i++ {
		SafeAddUint64(uint64(i), 1000)
	}
}

func BenchmarkSafeMulUint64(b *testing.B) {
	for i := 0; i < b.N; i++ {
		SafeMulUint64(uint64(i), 1000)
	}
}

func BenchmarkSafeSubUint64(b *testing.B) {
	for i := 0; i < b.N; i++ {
		SafeSubUint64(math.MaxUint64, uint64(i))
	}
}
