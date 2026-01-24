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
			result := MustSafeUint64ToInt64(tt.input)
			if result != tt.expected {
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
			result := MustSafeUint64ToInt(tt.input)
			if result != tt.expected {
				t.Errorf("MustSafeUint64ToInt(%d) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

// Benchmark tests

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
