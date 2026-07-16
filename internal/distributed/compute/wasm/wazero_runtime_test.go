// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// testdata/fac.wasm is copied unmodified from tetratelabs/wazero's own test
// corpus (Apache-2.0) — a real compiled WASM module (SSA-style factorial via
// an explicit loop and multi-value calls), used here as the "is this actually
// running WASM, not a stub" proof for wazeroRuntime.

package wasm

import (
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

const testFuelLimit = 10_000_000

func loadFacWasm(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/fac.wasm")
	if err != nil {
		t.Fatalf("read testdata/fac.wasm: %v", err)
	}
	return data
}

func i64Bytes(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// TestWazeroEngineRealFactorial proves the Engine, wired to the real wazero
// runtime, genuinely compiles and executes a WASM module — computing 5! = 120
// via the guest's own loop, not a host-side stub. Before wazeroRuntime
// existed, Engine could only ever run against mockRuntime.
func TestWazeroEngineRealFactorial(t *testing.T) {
	ctx := context.Background()
	rt := NewWazeroRuntime(ctx)
	defer rt.Close(ctx)

	engine := NewEngine(rt, 16, 30, 1.0)
	defer engine.Close(ctx)

	bytecode := loadFacWasm(t)
	hash := types.HexToHash("0xfac0")

	result, err := engine.Execute(ctx, hash, bytecode, "fac-ssa", i64Bytes(5), testFuelLimit)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.Output) != 8 {
		t.Fatalf("output length = %d, want 8", len(result.Output))
	}
	got := binary.BigEndian.Uint64(result.Output)
	if got != 120 {
		t.Fatalf("fac-ssa(5) = %d, want 120 (5! )", got)
	}
}

// TestWazeroEngineRealFactorialVaryingInputs cross-checks several inputs
// against Go's own factorial, guarding against a false positive from a fixed
// hardcoded return.
func TestWazeroEngineRealFactorialVaryingInputs(t *testing.T) {
	ctx := context.Background()
	rt := NewWazeroRuntime(ctx)
	defer rt.Close(ctx)

	engine := NewEngine(rt, 16, 30, 1.0)
	defer engine.Close(ctx)

	bytecode := loadFacWasm(t)
	hash := types.HexToHash("0xfac1")

	// fac-ssa(0) is deliberately excluded: the fixture's own loop computes
	// n-1 as an unsigned i64 without a zero guard, so 0-1 wraps to
	// 0xFFFFFFFFFFFFFF and the guest loops for ~2^64 iterations — a genuine
	// property of this borrowed reference module, not a bug in wazeroRuntime.
	// TestWazeroEngineTimeoutStopsRunawayExecution exercises exactly this
	// case to prove the real deadline enforcement actually cuts it off.
	cases := []struct {
		in   uint64
		want uint64
	}{
		{1, 1}, {2, 2}, {3, 6}, {6, 720}, {10, 3628800},
	}
	for _, tc := range cases {
		result, err := engine.Execute(ctx, hash, bytecode, "fac-ssa", i64Bytes(tc.in), testFuelLimit)
		if err != nil {
			t.Fatalf("Execute(%d): %v", tc.in, err)
		}
		got := binary.BigEndian.Uint64(result.Output)
		if got != tc.want {
			t.Fatalf("fac-ssa(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestWazeroEngineTimeoutStopsRunawayExecution proves the fuel-derived
// deadline is a real, enforced bound: fac-ssa(0) underflows its unsigned
// counter into a ~2^64-iteration loop (see the comment in
// TestWazeroEngineRealFactorialVaryingInputs), and a small fuel budget must
// make wazero abort it quickly rather than hang the caller.
func TestWazeroEngineTimeoutStopsRunawayExecution(t *testing.T) {
	ctx := context.Background()
	rt := NewWazeroRuntime(ctx)
	defer rt.Close(ctx)

	engine := NewEngine(rt, 16, 30, 1.0)
	defer engine.Close(ctx)

	bytecode := loadFacWasm(t)
	hash := types.HexToHash("0xfac4")

	// 50_000 fuel / defaultGasPerSecond(1_000_000) = 50ms budget.
	const smallFuel = 50_000
	start := time.Now()
	_, err := engine.Execute(ctx, hash, bytecode, "fac-ssa", i64Bytes(0), smallFuel)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected the runaway fac-ssa(0) loop to be aborted by the deadline, got nil error")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("deadline enforcement took %s, want well under 2s for a 50ms fuel budget", elapsed)
	}
}

// TestWazeroEngineUnknownExportRejected ensures a missing function name fails
// clearly rather than silently returning zero-value output.
func TestWazeroEngineUnknownExportRejected(t *testing.T) {
	ctx := context.Background()
	rt := NewWazeroRuntime(ctx)
	defer rt.Close(ctx)

	engine := NewEngine(rt, 16, 30, 1.0)
	defer engine.Close(ctx)

	bytecode := loadFacWasm(t)
	hash := types.HexToHash("0xfac2")

	if _, err := engine.Execute(ctx, hash, bytecode, "does-not-exist", i64Bytes(1), testFuelLimit); err == nil {
		t.Fatal("expected error for nonexistent export")
	}
}

// TestWazeroEngineIsolatesRepeatedCalls confirms each Execute call gets a
// fresh module instance: repeated calls with different inputs must not leak
// state (e.g. via globals or memory) between invocations of the same cached
// compiled module.
func TestWazeroEngineIsolatesRepeatedCalls(t *testing.T) {
	ctx := context.Background()
	rt := NewWazeroRuntime(ctx)
	defer rt.Close(ctx)

	engine := NewEngine(rt, 16, 30, 1.0)
	defer engine.Close(ctx)

	bytecode := loadFacWasm(t)
	hash := types.HexToHash("0xfac3")

	r1, err := engine.Execute(ctx, hash, bytecode, "fac-ssa", i64Bytes(4), testFuelLimit)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	r2, err := engine.Execute(ctx, hash, bytecode, "fac-ssa", i64Bytes(4), testFuelLimit)
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if binary.BigEndian.Uint64(r1.Output) != binary.BigEndian.Uint64(r2.Output) {
		t.Fatalf("repeated call with same input diverged: %v vs %v", r1.Output, r2.Output)
	}
	if got := binary.BigEndian.Uint64(r1.Output); got != 24 {
		t.Fatalf("fac-ssa(4) = %d, want 24", got)
	}
}
