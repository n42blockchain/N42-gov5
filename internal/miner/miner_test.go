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

package miner

import (
	"testing"

	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// CalcGasLimit Tests
// =============================================================================

func TestCalcGasLimitIncrease(t *testing.T) {
	// When desired > parent, should increase towards desired
	parent := uint64(10000000)
	desired := uint64(15000000)

	result := CalcGasLimit(parent, desired)

	if result <= parent {
		t.Errorf("CalcGasLimit should increase when desired > parent, got %d", result)
	}
	if result > desired {
		t.Errorf("CalcGasLimit should not exceed desired, got %d > %d", result, desired)
	}

	t.Logf("✓ CalcGasLimit increases correctly: %d -> %d (desired: %d)", parent, result, desired)
}

func TestCalcGasLimitDecrease(t *testing.T) {
	// When desired < parent, should decrease towards desired
	parent := uint64(15000000)
	desired := uint64(10000000)

	result := CalcGasLimit(parent, desired)

	if result >= parent {
		t.Errorf("CalcGasLimit should decrease when desired < parent, got %d", result)
	}
	if result < desired {
		t.Errorf("CalcGasLimit should not go below desired, got %d < %d", result, desired)
	}

	t.Logf("✓ CalcGasLimit decreases correctly: %d -> %d (desired: %d)", parent, result, desired)
}

func TestCalcGasLimitEqual(t *testing.T) {
	// When desired == parent, should stay the same
	parent := uint64(10000000)
	desired := uint64(10000000)

	result := CalcGasLimit(parent, desired)

	if result != desired {
		t.Errorf("CalcGasLimit should equal desired when parent == desired, got %d", result)
	}

	t.Logf("✓ CalcGasLimit handles equal values correctly")
}

func TestCalcGasLimitMinGasLimit(t *testing.T) {
	// When desired is below MinGasLimit, should use MinGasLimit
	parent := uint64(10000000)
	desired := uint64(1000) // Below MinGasLimit

	result := CalcGasLimit(parent, desired)

	if result < params.MinGasLimit {
		t.Errorf("CalcGasLimit should not go below MinGasLimit, got %d < %d", result, params.MinGasLimit)
	}

	t.Logf("✓ CalcGasLimit respects MinGasLimit: %d (min: %d)", result, params.MinGasLimit)
}

func TestCalcGasLimitDelta(t *testing.T) {
	// Verify delta calculation
	parent := uint64(params.GasLimitBoundDivisor * 100)
	desired := uint64(params.GasLimitBoundDivisor * 200)

	result := CalcGasLimit(parent, desired)

	expectedDelta := parent/params.GasLimitBoundDivisor - 1
	expectedMax := parent + expectedDelta

	if result > expectedMax {
		t.Errorf("CalcGasLimit exceeded max delta, got %d > %d", result, expectedMax)
	}

	t.Logf("✓ CalcGasLimit delta calculation correct: delta=%d", expectedDelta)
}

func TestCalcGasLimitGradualIncrease(t *testing.T) {
	// Test that gas limit increases gradually
	parent := uint64(10000000)
	desired := uint64(20000000)

	current := parent
	iterations := 0
	maxIterations := 10000

	for current < desired && iterations < maxIterations {
		next := CalcGasLimit(current, desired)
		if next <= current {
			t.Errorf("Gas limit should increase on each iteration, got %d <= %d", next, current)
			break
		}
		current = next
		iterations++
	}

	if current < desired && iterations >= maxIterations {
		t.Logf("Warning: Did not reach desired in %d iterations (current: %d, desired: %d)", maxIterations, current, desired)
	}

	t.Logf("✓ CalcGasLimit increases gradually over %d iterations", iterations)
}

func TestCalcGasLimitGradualDecrease(t *testing.T) {
	// Test that gas limit decreases gradually
	parent := uint64(20000000)
	desired := uint64(10000000)

	current := parent
	iterations := 0
	maxIterations := 10000

	for current > desired && iterations < maxIterations {
		next := CalcGasLimit(current, desired)
		if next >= current {
			t.Errorf("Gas limit should decrease on each iteration, got %d >= %d", next, current)
			break
		}
		current = next
		iterations++
	}

	if current > desired && iterations >= maxIterations {
		t.Logf("Warning: Did not reach desired in %d iterations (current: %d, desired: %d)", maxIterations, current, desired)
	}

	t.Logf("✓ CalcGasLimit decreases gradually over %d iterations", iterations)
}

func TestCalcGasLimitBoundary(t *testing.T) {
	tests := []struct {
		name    string
		parent  uint64
		desired uint64
	}{
		{"very_small_parent", params.MinGasLimit, params.MinGasLimit * 2},
		{"large_parent", 100000000, 50000000},
		{"same_values", 10000000, 10000000},
		{"desired_zero", 10000000, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalcGasLimit(tt.parent, tt.desired)

			// Result should always be >= MinGasLimit
			if result < params.MinGasLimit {
				t.Errorf("Result %d < MinGasLimit %d", result, params.MinGasLimit)
			}

			// Result should be a reasonable value
			if result == 0 {
				t.Error("Result should not be zero")
			}
		})
	}

	t.Logf("✓ CalcGasLimit handles boundary cases correctly")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkCalcGasLimit(b *testing.B) {
	parent := uint64(10000000)
	desired := uint64(15000000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CalcGasLimit(parent, desired)
	}
}

func BenchmarkCalcGasLimitDecrease(b *testing.B) {
	parent := uint64(15000000)
	desired := uint64(10000000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CalcGasLimit(parent, desired)
	}
}

func BenchmarkCalcGasLimitEqual(b *testing.B) {
	parent := uint64(10000000)
	desired := uint64(10000000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CalcGasLimit(parent, desired)
	}
}

func BenchmarkCalcGasLimitIteration(b *testing.B) {
	parent := uint64(10000000)
	desired := uint64(20000000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		current := parent
		for j := 0; j < 100; j++ {
			current = CalcGasLimit(current, desired)
		}
	}
}

