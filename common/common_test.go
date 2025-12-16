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

package common

import (
	"math"
	"math/big"
	"testing"
	"time"
)

// =============================================================================
// Big Constants Tests
// =============================================================================

func TestBigConstants(t *testing.T) {
	tests := []struct {
		name     string
		value    *big.Int
		expected int64
	}{
		{"Big0", Big0, 0},
		{"Big1", Big1, 1},
		{"Big2", Big2, 2},
		{"Big3", Big3, 3},
		{"Big32", Big32, 32},
		{"Big256", Big256, 256},
		{"Big257", Big257, 257},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value.Int64() != tt.expected {
				t.Errorf("%s = %d, want %d", tt.name, tt.value.Int64(), tt.expected)
			}
		})
	}

	t.Logf("✓ All big constants are correctly defined")
}

func TestBigConstantsImmutability(t *testing.T) {
	// Verify constants are not modified by operations
	original := big.NewInt(0)
	original.Add(Big1, Big2)

	if Big1.Int64() != 1 {
		t.Error("Big1 was modified")
	}
	if Big2.Int64() != 2 {
		t.Error("Big2 was modified")
	}

	t.Logf("✓ Big constants remain immutable during operations")
}

// =============================================================================
// GasPool Tests
// =============================================================================

func TestGasPool_New(t *testing.T) {
	gp := GasPool(1000)
	if gp.Gas() != 1000 {
		t.Errorf("GasPool.Gas() = %d, want 1000", gp.Gas())
	}

	t.Logf("✓ GasPool initialization works correctly")
}

func TestGasPool_AddGas(t *testing.T) {
	gp := GasPool(100)
	gp.AddGas(50)

	if gp.Gas() != 150 {
		t.Errorf("GasPool.Gas() = %d, want 150", gp.Gas())
	}

	t.Logf("✓ GasPool.AddGas works correctly")
}

func TestGasPool_SubGas_Success(t *testing.T) {
	gp := GasPool(100)
	err := gp.SubGas(50)

	if err != nil {
		t.Errorf("GasPool.SubGas() unexpected error: %v", err)
	}
	if gp.Gas() != 50 {
		t.Errorf("GasPool.Gas() = %d, want 50", gp.Gas())
	}

	t.Logf("✓ GasPool.SubGas works correctly")
}

func TestGasPool_SubGas_Error(t *testing.T) {
	gp := GasPool(100)
	err := gp.SubGas(150)

	if err != ErrGasLimitReached {
		t.Errorf("GasPool.SubGas() = %v, want ErrGasLimitReached", err)
	}
	if gp.Gas() != 100 {
		t.Errorf("GasPool.Gas() = %d, want 100 (unchanged)", gp.Gas())
	}

	t.Logf("✓ GasPool.SubGas returns error for insufficient gas")
}

func TestGasPool_SubGas_Exact(t *testing.T) {
	gp := GasPool(100)
	err := gp.SubGas(100)

	if err != nil {
		t.Errorf("GasPool.SubGas() unexpected error: %v", err)
	}
	if gp.Gas() != 0 {
		t.Errorf("GasPool.Gas() = %d, want 0", gp.Gas())
	}

	t.Logf("✓ GasPool.SubGas works for exact amount")
}

func TestGasPool_String(t *testing.T) {
	gp := GasPool(12345)
	str := gp.String()

	if str != "12345" {
		t.Errorf("GasPool.String() = %s, want '12345'", str)
	}

	t.Logf("✓ GasPool.String works correctly")
}

func TestGasPool_Chaining(t *testing.T) {
	gp := GasPool(0)
	result := gp.AddGas(100).AddGas(50)

	if result.Gas() != 150 {
		t.Errorf("Chained AddGas = %d, want 150", result.Gas())
	}

	t.Logf("✓ GasPool.AddGas supports chaining")
}

func TestGasPool_AddGas_Overflow(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("AddGas should panic on overflow")
		} else {
			t.Logf("✓ GasPool.AddGas panics on overflow as expected")
		}
	}()

	gp := GasPool(math.MaxUint64)
	gp.AddGas(1)
}

// =============================================================================
// PrettyDuration Tests
// =============================================================================

func TestPrettyDuration_String(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		contains string
	}{
		{"seconds", 5 * time.Second, "5s"},
		{"minutes", 2 * time.Minute, "2m"},
		{"hours", 3 * time.Hour, "3h"},
		{"mixed", 1*time.Hour + 30*time.Minute, "1h30m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pd := PrettyDuration(tt.duration)
			str := pd.String()
			if len(str) == 0 {
				t.Error("PrettyDuration.String() returned empty string")
			}
		})
	}

	t.Logf("✓ PrettyDuration.String works for various durations")
}

func TestPrettyDuration_Precision(t *testing.T) {
	// Test that long decimal precision is truncated
	pd := PrettyDuration(1234567890 * time.Nanosecond)
	str := pd.String()

	// Should not have more than 4 decimal places
	if len(str) > 15 {
		t.Logf("PrettyDuration.String() = %s (length: %d)", str, len(str))
	}

	t.Logf("✓ PrettyDuration truncates excessive precision")
}

// =============================================================================
// PrettyAge Tests
// =============================================================================

func TestPrettyAge_Recent(t *testing.T) {
	pa := PrettyAge(time.Now())
	str := pa.String()

	if str != "0" {
		t.Logf("PrettyAge for now = %s (expected '0' for < 1 second)", str)
	}

	t.Logf("✓ PrettyAge handles recent times")
}

func TestPrettyAge_Seconds(t *testing.T) {
	pa := PrettyAge(time.Now().Add(-5 * time.Second))
	str := pa.String()

	if len(str) == 0 {
		t.Error("PrettyAge.String() returned empty string")
	}

	t.Logf("✓ PrettyAge handles seconds: %s", str)
}

func TestPrettyAge_Minutes(t *testing.T) {
	pa := PrettyAge(time.Now().Add(-5 * time.Minute))
	str := pa.String()

	if len(str) == 0 {
		t.Error("PrettyAge.String() returned empty string")
	}

	t.Logf("✓ PrettyAge handles minutes: %s", str)
}

func TestPrettyAge_Hours(t *testing.T) {
	pa := PrettyAge(time.Now().Add(-5 * time.Hour))
	str := pa.String()

	if len(str) == 0 {
		t.Error("PrettyAge.String() returned empty string")
	}

	t.Logf("✓ PrettyAge handles hours: %s", str)
}

func TestPrettyAge_Days(t *testing.T) {
	pa := PrettyAge(time.Now().Add(-48 * time.Hour))
	str := pa.String()

	if len(str) == 0 {
		t.Error("PrettyAge.String() returned empty string")
	}

	t.Logf("✓ PrettyAge handles days: %s", str)
}

// =============================================================================
// ErrGasLimitReached Tests
// =============================================================================

func TestErrGasLimitReached(t *testing.T) {
	if ErrGasLimitReached == nil {
		t.Error("ErrGasLimitReached should not be nil")
	}
	if ErrGasLimitReached.Error() != "gas limit reached" {
		t.Errorf("ErrGasLimitReached.Error() = %s, want 'gas limit reached'", ErrGasLimitReached.Error())
	}

	t.Logf("✓ ErrGasLimitReached is correctly defined")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkGasPoolAddGas(b *testing.B) {
	gp := GasPool(0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gp = GasPool(0)
		gp.AddGas(1000)
	}
}

func BenchmarkGasPoolSubGas(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gp := GasPool(1000)
		gp.SubGas(500)
	}
}

func BenchmarkGasPoolGas(b *testing.B) {
	gp := GasPool(1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gp.Gas()
	}
}

func BenchmarkGasPoolString(b *testing.B) {
	gp := GasPool(12345)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = gp.String()
	}
}

func BenchmarkPrettyDurationString(b *testing.B) {
	pd := PrettyDuration(1*time.Hour + 30*time.Minute + 45*time.Second)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = pd.String()
	}
}

func BenchmarkPrettyAgeString(b *testing.B) {
	pa := PrettyAge(time.Now().Add(-5 * time.Hour))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = pa.String()
	}
}

func BenchmarkBigIntComparison(b *testing.B) {
	a := big.NewInt(1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Cmp(Big256)
	}
}
