// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers ExecutionResult's small accessors (Unwrap/Return/Revert), the
// formatUint256/toWordSize formatting helpers, QMDBSingleFoldEnabled's env
// parsing, and CheckSealParentApplied's no-op path when QMDB is disabled.

package internal

import (
	"errors"
	"math"
	"os"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	vm2 "github.com/n42blockchain/N42/internal/vm"
)

func TestExecutionResultUnwrapReturnRevert(t *testing.T) {
	boom := errors.New("boom")
	failed := &ExecutionResult{Err: boom, ReturnData: []byte{0x01}}
	if failed.Unwrap() != boom {
		t.Fatalf("Unwrap() = %v, want %v", failed.Unwrap(), boom)
	}
	if !failed.Failed() {
		t.Fatalf("Failed() = false, want true")
	}
	if got := failed.Return(); got != nil {
		t.Fatalf("Return() on a failed result = %v, want nil", got)
	}
	if got := failed.Revert(); got != nil {
		t.Fatalf("Revert() for a non-revert error = %v, want nil", got)
	}

	ok := &ExecutionResult{ReturnData: []byte{0xaa, 0xbb}}
	if ok.Failed() {
		t.Fatalf("Failed() on a nil-error result = true, want false")
	}
	if got := ok.Return(); len(got) != 2 || got[0] != 0xaa {
		t.Fatalf("Return() = %v, want copy of ReturnData", got)
	}
	// Return() must hand back a copy, not an alias.
	got := ok.Return()
	got[0] = 0xff
	if ok.ReturnData[0] != 0xaa {
		t.Fatalf("Return() exposed the underlying slice; mutation leaked")
	}

	reverted := &ExecutionResult{Err: vm2.ErrExecutionReverted, ReturnData: []byte{0x01, 0x02}}
	if got := reverted.Revert(); len(got) != 2 {
		t.Fatalf("Revert() on an ErrExecutionReverted result = %v, want the return data", got)
	}
	if got := reverted.Return(); got != nil {
		t.Fatalf("Return() on a reverted (failed) result = %v, want nil", got)
	}
}

func TestFormatUint256(t *testing.T) {
	if got := formatUint256(nil); got != "<nil>" {
		t.Fatalf("formatUint256(nil) = %q, want \"<nil>\"", got)
	}
	v := uint256.NewInt(42)
	if got := formatUint256(v); got != v.String() {
		t.Fatalf("formatUint256(42) = %q, want %q", got, v.String())
	}
}

func TestToWordSize(t *testing.T) {
	cases := []struct {
		size uint64
		want uint64
	}{
		{0, 0},
		{1, 1},
		{31, 1},
		{32, 1},
		{33, 2},
		{64, 2},
	}
	for _, c := range cases {
		if got := toWordSize(c.size); got != c.want {
			t.Fatalf("toWordSize(%d) = %d, want %d", c.size, got, c.want)
		}
	}
	// Overflow guard: a size near MaxUint64 must not wrap around.
	if got := toWordSize(math.MaxUint64); got != math.MaxUint64/32+1 {
		t.Fatalf("toWordSize(MaxUint64) = %d, want %d", got, math.MaxUint64/32+1)
	}
}

func TestQMDBSingleFoldEnabledParsing(t *testing.T) {
	cases := map[string]bool{
		"1":    true,
		"true": true,
		"TRUE": true,
		"yes":  true,
		"on":   true,
		"0":    false,
		"off":  false,
		"":     false,
		"junk": false,
	}
	for in, want := range cases {
		if got := parseQMDBSingleFold(in); got != want {
			t.Fatalf("parseQMDBSingleFold(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestQMDBSingleFoldEnabledMemoizesFirstEnvRead(t *testing.T) {
	// sync.Once means this only proves the function is callable and stable
	// across repeated calls, not that it re-reads the env each time.
	prev, had := os.LookupEnv("N42_QMDB_SINGLE_FOLD")
	if had {
		defer os.Setenv("N42_QMDB_SINGLE_FOLD", prev)
	}
	first := QMDBSingleFoldEnabled()
	second := QMDBSingleFoldEnabled()
	if first != second {
		t.Fatalf("QMDBSingleFoldEnabled() not stable across calls: %v then %v", first, second)
	}
}

func TestCheckSealParentAppliedNoopWhenQMDBDisabled(t *testing.T) {
	bc := &BlockChain{}
	blk := testConcreteBlock(&block.Header{Number: uint256.NewInt(1)}, &block.Body{})
	if err := bc.CheckSealParentApplied(blk); err != nil {
		t.Fatalf("CheckSealParentApplied() with QMDB disabled = %v, want nil", err)
	}
	if err := bc.CheckSealParentApplied(nil); err != nil {
		t.Fatalf("CheckSealParentApplied(nil) = %v, want nil", err)
	}
}
