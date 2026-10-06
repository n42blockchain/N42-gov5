// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"testing"

	"github.com/n42blockchain/N42/params"
)

func TestCalcGasLimitMovesTowardDesired(t *testing.T) {
	// parent below desired: should increase but not overshoot.
	got := CalcGasLimit(1_000_000, 2_000_000)
	if got <= 1_000_000 || got > 2_000_000 {
		t.Fatalf("expected gas limit to increase toward desired without overshoot, got %d", got)
	}

	// parent above desired: should decrease but not undershoot.
	got2 := CalcGasLimit(2_000_000, 1_000_000)
	if got2 >= 2_000_000 || got2 < 1_000_000 {
		t.Fatalf("expected gas limit to decrease toward desired without undershoot, got %d", got2)
	}

	// parent equals desired: unchanged.
	if got3 := CalcGasLimit(1_500_000, 1_500_000); got3 != 1_500_000 {
		t.Fatalf("expected unchanged gas limit when parent == desired, got %d", got3)
	}
}

func TestCalcGasLimitEnforcesMinimum(t *testing.T) {
	got := CalcGasLimit(params.MinGasLimit, 1)
	if got < params.MinGasLimit {
		t.Fatalf("expected desired limit floored at MinGasLimit, got %d", got)
	}
}

func TestCalcGasLimitHandlesSmallParentNoUnderflow(t *testing.T) {
	// parentGasLimit smaller than GasLimitBoundDivisor: delta must clamp to 0,
	// not underflow.
	got := CalcGasLimit(10, 5_000_000)
	if got < 10 {
		t.Fatalf("expected no underflow, got %d", got)
	}
}

func TestCalcGasLimitStressModeJumpsInstantly(t *testing.T) {
	old := stressGasLimit
	stressGasLimit = true
	defer func() { stressGasLimit = old }()

	got := CalcGasLimit(1_000_000, 9_000_000)
	if got != 9_000_000 {
		t.Fatalf("expected instant jump to desired limit in stress mode, got %d", got)
	}
}

func TestCalcGasLimitDecreaseUnderflowGuard(t *testing.T) {
	// parentGasLimit just above GasLimitBoundDivisor means delta is small
	// (~0-1); desired is far below parent. The guarded branch must never wrap
	// around to a huge uint64 value — the result must stay sane and bounded by
	// the parent limit.
	got := CalcGasLimit(params.GasLimitBoundDivisor, 1)
	if got == 0 || got > params.GasLimitBoundDivisor {
		t.Fatalf("expected a sane bounded result with no underflow, got %d", got)
	}
}

func TestFillGasBudgetDefaultsToHeaderLimit(t *testing.T) {
	old := fillGas
	fillGas = 0
	defer func() { fillGas = old }()

	if got := fillGasBudget(30_000_000); got != 30_000_000 {
		t.Fatalf("expected header limit when fillGas unset, got %d", got)
	}
}

func TestFillGasBudgetCapsAtFillGasWhenLower(t *testing.T) {
	old := fillGas
	fillGas = 10_000_000
	defer func() { fillGas = old }()

	if got := fillGasBudget(30_000_000); got != 10_000_000 {
		t.Fatalf("expected capped fill gas 10_000_000, got %d", got)
	}
	// When fillGas exceeds the header limit, the header limit wins.
	if got := fillGasBudget(5_000_000); got != 5_000_000 {
		t.Fatalf("expected header limit to win when lower than fillGas, got %d", got)
	}
}
