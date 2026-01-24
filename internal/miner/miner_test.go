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
	"time"

	"github.com/n42blockchain/N42/common/block"
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

// =============================================================================
// signalToErr Tests
// =============================================================================

func TestSignalToErr(t *testing.T) {
	tests := []struct {
		name        string
		signal      int32
		expectedErr error
	}{
		{"commitInterruptNewHead", commitInterruptNewHead, errBlockInterruptedByNewHead},
		{"commitInterruptResubmit", commitInterruptResubmit, errBlockInterruptedByRecommit},
		{"commitInterruptTimeout", commitInterruptTimeout, errBlockInterruptedByTimeout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := signalToErr(tt.signal)
			if err != tt.expectedErr {
				t.Errorf("signalToErr(%d) = %v, want %v", tt.signal, err, tt.expectedErr)
			}
		})
	}

	t.Logf("✓ signalToErr handles known signals correctly")
}

func TestSignalToErrUnknown(t *testing.T) {
	err := signalToErr(999)
	if err == nil {
		t.Error("signalToErr should return error for unknown signal")
	}
	if err == errBlockInterruptedByNewHead || err == errBlockInterruptedByRecommit || err == errBlockInterruptedByTimeout {
		t.Error("signalToErr should not return known error for unknown signal")
	}

	t.Logf("✓ signalToErr handles unknown signals correctly: %v", err)
}

func TestSignalToErrNone(t *testing.T) {
	err := signalToErr(commitInterruptNone)
	if err == nil {
		t.Error("signalToErr should return error for commitInterruptNone")
	}

	t.Logf("✓ signalToErr handles commitInterruptNone correctly: %v", err)
}

// =============================================================================
// copyReceipts Tests
// =============================================================================

func TestCopyReceiptsEmpty(t *testing.T) {
	receipts := []*block.Receipt{}
	copied := copyReceipts(receipts)

	if len(copied) != 0 {
		t.Errorf("copyReceipts of empty slice should be empty, got length %d", len(copied))
	}

	t.Logf("✓ copyReceipts handles empty slice correctly")
}

func TestCopyReceiptsNil(t *testing.T) {
	var receipts []*block.Receipt
	copied := copyReceipts(receipts)

	if copied == nil {
		t.Error("copyReceipts should return empty slice, not nil")
	}
	if len(copied) != 0 {
		t.Errorf("copyReceipts of nil should be empty, got length %d", len(copied))
	}

	t.Logf("✓ copyReceipts handles nil slice correctly")
}

func TestCopyReceiptsDeepCopy(t *testing.T) {
	receipts := []*block.Receipt{
		{
			Status:            1,
			CumulativeGasUsed: 21000,
			GasUsed:           21000,
		},
		{
			Status:            1,
			CumulativeGasUsed: 42000,
			GasUsed:           21000,
		},
	}

	copied := copyReceipts(receipts)

	// Verify length
	if len(copied) != len(receipts) {
		t.Fatalf("copyReceipts length mismatch: got %d, want %d", len(copied), len(receipts))
	}

	// Verify values
	for i := range receipts {
		if copied[i].Status != receipts[i].Status {
			t.Errorf("Receipt[%d].Status mismatch", i)
		}
		if copied[i].CumulativeGasUsed != receipts[i].CumulativeGasUsed {
			t.Errorf("Receipt[%d].CumulativeGasUsed mismatch", i)
		}
	}

	// Verify deep copy (modifying copied doesn't affect original)
	copied[0].Status = 0
	if receipts[0].Status == 0 {
		t.Error("copyReceipts should create deep copies")
	}

	t.Logf("✓ copyReceipts creates deep copies correctly")
}

// =============================================================================
// Constants Tests
// =============================================================================

func TestCommitInterruptConstants(t *testing.T) {
	// Verify constants are distinct
	constants := map[int32]string{
		commitInterruptNone:      "commitInterruptNone",
		commitInterruptNewHead:   "commitInterruptNewHead",
		commitInterruptResubmit:  "commitInterruptResubmit",
		commitInterruptTimeout:   "commitInterruptTimeout",
	}

	values := make(map[int32]bool)
	for val, name := range constants {
		if values[val] {
			t.Errorf("Duplicate constant value for %s: %d", name, val)
		}
		values[val] = true
	}

	// Verify ordering (None should be 0)
	if commitInterruptNone != 0 {
		t.Errorf("commitInterruptNone should be 0, got %d", commitInterruptNone)
	}

	t.Logf("✓ Commit interrupt constants are distinct and correctly ordered")
}

func TestIntervalConstants(t *testing.T) {
	if minPeriodInterval <= 0 {
		t.Error("minPeriodInterval should be positive")
	}
	if maxRecommitInterval <= 0 {
		t.Error("maxRecommitInterval should be positive")
	}
	if maxRecommitInterval < minPeriodInterval {
		t.Error("maxRecommitInterval should be >= minPeriodInterval")
	}
	if staleThreshold <= 0 {
		t.Error("staleThreshold should be positive")
	}

	t.Logf("✓ Interval constants are valid: minPeriod=%v, maxRecommit=%v, staleThreshold=%d",
		minPeriodInterval, maxRecommitInterval, staleThreshold)
}

// =============================================================================
// Error Variables Tests
// =============================================================================

func TestErrorVariables(t *testing.T) {
	errors := []error{
		errBlockInterruptedByNewHead,
		errBlockInterruptedByRecommit,
		errBlockInterruptedByTimeout,
	}

	for _, err := range errors {
		if err == nil {
			t.Error("Error variable should not be nil")
		}
		if err.Error() == "" {
			t.Error("Error message should not be empty")
		}
	}

	t.Logf("✓ Error variables are properly defined")
}

// =============================================================================
// recalcRecommit Tests
// =============================================================================

func TestRecalcRecommitIncrease(t *testing.T) {
	minRecommit := time.Second
	prev := time.Second * 2
	target := float64(time.Second * 3)

	result := recalcRecommit(minRecommit, prev, target, true)

	// When increasing, result should be between prev and maxRecommitInterval
	if result < prev {
		t.Errorf("recalcRecommit with inc=true should not decrease, got %v < %v", result, prev)
	}
	if result > maxRecommitInterval {
		t.Errorf("recalcRecommit should not exceed maxRecommitInterval, got %v > %v", result, maxRecommitInterval)
	}

	t.Logf("✓ recalcRecommit increases correctly: %v -> %v", prev, result)
}

func TestRecalcRecommitDecrease(t *testing.T) {
	minRecommit := time.Second
	prev := time.Second * 5
	target := float64(time.Second * 2)

	result := recalcRecommit(minRecommit, prev, target, false)

	// When decreasing, result should be between minRecommit and prev
	if result > prev {
		t.Errorf("recalcRecommit with inc=false should not increase, got %v > %v", result, prev)
	}
	if result < minRecommit {
		t.Errorf("recalcRecommit should not go below minRecommit, got %v < %v", result, minRecommit)
	}

	t.Logf("✓ recalcRecommit decreases correctly: %v -> %v", prev, result)
}

func TestRecalcRecommitBoundaryMax(t *testing.T) {
	minRecommit := time.Second
	prev := maxRecommitInterval - time.Millisecond
	target := float64(maxRecommitInterval * 2)

	result := recalcRecommit(minRecommit, prev, target, true)

	if result > maxRecommitInterval {
		t.Errorf("recalcRecommit should be capped at maxRecommitInterval, got %v", result)
	}

	t.Logf("✓ recalcRecommit respects max boundary: %v", result)
}

func TestRecalcRecommitBoundaryMin(t *testing.T) {
	minRecommit := time.Second * 2
	prev := minRecommit + time.Millisecond
	target := float64(time.Millisecond)

	result := recalcRecommit(minRecommit, prev, target, false)

	if result < minRecommit {
		t.Errorf("recalcRecommit should not go below minRecommit, got %v < %v", result, minRecommit)
	}

	t.Logf("✓ recalcRecommit respects min boundary: %v", result)
}

func TestRecalcRecommitStability(t *testing.T) {
	// Test that recommit converges towards target
	minRecommit := time.Second
	target := float64(time.Second * 3)
	prev := time.Second * 2

	// Apply multiple iterations
	for i := 0; i < 10; i++ {
		prev = recalcRecommit(minRecommit, prev, target, true)
	}

	// Should converge towards target (with some tolerance due to bias)
	diff := float64(prev.Nanoseconds()) - target
	tolerance := target * 0.5 // 50% tolerance

	if diff < -tolerance || diff > tolerance+float64(intervalAdjustBias) {
		t.Errorf("recalcRecommit should converge towards target, got %v (target: %v)", prev, time.Duration(int64(target)))
	}

	t.Logf("✓ recalcRecommit converges: final=%v, target=%v", prev, time.Duration(int64(target)))
}

func BenchmarkRecalcRecommit(b *testing.B) {
	minRecommit := time.Second
	prev := time.Second * 2
	target := float64(time.Second * 3)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recalcRecommit(minRecommit, prev, target, i%2 == 0)
	}
}

