// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// testdata/fac.wasm is copied from tetratelabs/wazero's own test corpus
// (Apache-2.0), same fixture used in internal/distributed/compute/wasm's
// wazero_runtime_test.go.

package inference

import (
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

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

// TestWazeroExecutorRealFactorial proves WazeroExecutor.Execute genuinely
// runs the guest module rather than returning a stub, using the same
// real fac-ssa WASM export exercised in the wasm package's own tests.
func TestWazeroExecutorRealFactorial(t *testing.T) {
	ctx := context.Background()
	exec := NewWazeroExecutor(ctx, WazeroExecutorConfig{FuncName: "fac-ssa"})
	defer exec.Close(ctx)

	modelHash := types.HexToHash("0xfac00d")
	if err := exec.LoadModel(modelHash, loadFacWasm(t)); err != nil {
		t.Fatalf("LoadModel: %v", err)
	}

	result, err := exec.Execute(ctx, modelHash, i64Bytes(6))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := binary.BigEndian.Uint64(result.Output); got != 720 {
		t.Fatalf("fac-ssa(6) = %d, want 720", got)
	}
	if result.ModelVersion != "wazero-v1" {
		t.Fatalf("ModelVersion = %q, want wazero-v1", result.ModelVersion)
	}
}

// TestWazeroExecutorUnknownModel confirms Execute fails closed for a model
// that was never loaded (no silent success on an empty backend).
func TestWazeroExecutorUnknownModel(t *testing.T) {
	ctx := context.Background()
	exec := NewWazeroExecutor(ctx, WazeroExecutorConfig{})
	defer exec.Close(ctx)

	if _, err := exec.Execute(ctx, types.HexToHash("0xdead"), i64Bytes(1)); err == nil {
		t.Fatal("expected error for unloaded model")
	}
}

// TestInferenceServiceEndToEndRealWASM is the full pipeline test: register a
// model in the ModelRegistry, load its real WASM bytecode into WazeroExecutor,
// submit a request through InferenceService (the same service the precompile
// adapter wraps), execute it, and confirm the cached result reflects genuine
// guest computation — not the keccak(model||input) placeholder that
// WASMExecutor (executor_wasm_stub.go) produces.
func TestInferenceServiceEndToEndRealWASM(t *testing.T) {
	ctx := context.Background()
	registry := NewModelRegistry()
	modelHash, err := registry.Register("fac-ssa-test", FormatCustom, uint64(len(loadFacWasm(t))), "v1", []string{"arithmetic"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	exec := NewWazeroExecutor(ctx, WazeroExecutorConfig{FuncName: "fac-ssa"})
	defer exec.Close(ctx)
	if err := exec.LoadModel(modelHash, loadFacWasm(t)); err != nil {
		t.Fatalf("LoadModel: %v", err)
	}

	svc := NewInferenceService(registry)
	svc.SetExecutor(exec)

	submitter := types.Address{0x42}
	requestID, err := svc.SubmitRequest(modelHash, i64Bytes(7), submitter)
	if err != nil {
		t.Fatalf("SubmitRequest: %v", err)
	}

	result, err := svc.Execute(ctx, requestID)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := binary.BigEndian.Uint64(result.Output); got != 5040 {
		t.Fatalf("fac-ssa(7) via InferenceService = %d, want 5040 (7!)", got)
	}

	cached, ok := svc.GetCachedResult(requestID)
	if !ok {
		t.Fatal("expected a cached result after Execute")
	}
	if cached.Status != RequestOptimisticVerified {
		t.Fatalf("cached status = %v, want RequestOptimisticVerified", cached.Status)
	}

	// FinalizeRequest transitions optimistic -> verified, as the opML
	// challenge-window lifecycle expects.
	if err := svc.FinalizeRequest(requestID); err != nil {
		t.Fatalf("FinalizeRequest: %v", err)
	}
	cached, ok = svc.GetCachedResult(requestID)
	if !ok || cached.Status != RequestVerified {
		t.Fatalf("cached status after finalize = %v, want RequestVerified", cached.Status)
	}
}

// TestInferenceServiceEndToEndRealWASMTimesOutOnRunaway confirms a runaway
// guest (fac-ssa(0)'s unsigned-underflow infinite loop, same as in the wasm
// package's tests) surfaces as a request failure through the full service
// stack rather than hanging the caller.
func TestInferenceServiceEndToEndRealWASMTimesOutOnRunaway(t *testing.T) {
	ctx := context.Background()
	registry := NewModelRegistry()
	modelHash, err := registry.Register("fac-ssa-runaway", FormatCustom, 0, "v1", nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	exec := NewWazeroExecutor(ctx, WazeroExecutorConfig{FuncName: "fac-ssa", FuelLimit: 50_000})
	defer exec.Close(ctx)
	if err := exec.LoadModel(modelHash, loadFacWasm(t)); err != nil {
		t.Fatalf("LoadModel: %v", err)
	}

	svc := NewInferenceService(registry)
	svc.SetExecutor(exec)

	requestID, err := svc.SubmitRequest(modelHash, i64Bytes(0), types.Address{})
	if err != nil {
		t.Fatalf("SubmitRequest: %v", err)
	}

	start := time.Now()
	_, err = svc.Execute(ctx, requestID)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected the runaway execution to fail")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("runaway execution took %s to fail, want well under 2s", elapsed)
	}
}
