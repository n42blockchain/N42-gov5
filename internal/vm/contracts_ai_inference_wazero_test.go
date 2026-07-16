// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// End-to-end proof that the 0x0301 precompile reaches real WASM execution
// through the exact wiring node.go performs (ModelRegistry + MemCAS +
// WazeroExecutor + InferenceService + PrecompileBackend), not a mock — the
// same real fac.wasm fixture used in the wasm/inference package tests.
// Before this wiring (see internal/node/node.go's startAIInference,
// internal/distributed/compute/inference/precompile_backend.go and
// executor_wazero.go), vm.SetInferenceBackend was called only from this
// package's own unit tests — every real requestInference call hit
// errAIInferenceNoBackend regardless of chain configuration.

package vm

import (
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/distributed/compute/inference"
)

func i64BytesVM(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// TestAIInferencePrecompileRealWASMEndToEnd drives the precompile exactly as
// an EVM call would: requestInference(modelHash, inputCAS, tier) -> requestID,
// then polls getResult(requestID) until the async execution (see
// PrecompileBackend.executeAsync) completes, and decodes the real
// wazero-computed factorial from outputCAS via the same CAS the backend used.
func TestAIInferencePrecompileRealWASMEndToEnd(t *testing.T) {
	facWasm, err := os.ReadFile("../distributed/compute/wasm/testdata/fac.wasm")
	if err != nil {
		t.Fatalf("read fac.wasm fixture: %v", err)
	}

	ctx := context.Background()
	models := inference.NewModelRegistry()
	modelHash, err := models.Register("fac-ssa", inference.FormatCustom, uint64(len(facWasm)), "v1", nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	executor := inference.NewWazeroExecutor(ctx, inference.WazeroExecutorConfig{FuncName: "fac-ssa"})
	defer executor.Close(ctx)
	if err := executor.LoadModel(modelHash, facWasm); err != nil {
		t.Fatalf("LoadModel: %v", err)
	}

	svc := inference.NewInferenceService(models)
	svc.SetExecutor(executor)

	cas := inference.NewMemCAS()
	inputCAS, err := cas.Store(i64BytesVM(6)) // fac-ssa(6) = 720
	if err != nil {
		t.Fatalf("cas.Store input: %v", err)
	}

	backend := inference.NewPrecompileBackend(svc, cas.Load, cas.Store)
	SetInferenceBackend(backend)
	defer SetInferenceBackend(nil)

	precompile := &aiInference{}

	// requestInference(modelHash, inputCAS, tier=0)
	reqInput := make([]byte, 1+65)
	reqInput[0] = aiRequestInference
	copy(reqInput[1:33], modelHash[:])
	copy(reqInput[33:65], inputCAS[:])
	reqInput[65] = 0 // tier

	reqOut, err := precompile.Run(reqInput)
	if err != nil {
		t.Fatalf("requestInference Run: %v", err)
	}
	if len(reqOut) != 32 {
		t.Fatalf("requestID output length = %d, want 32", len(reqOut))
	}
	var requestID types.Hash
	copy(requestID[:], reqOut)

	// Poll getResult(requestID) — the real execution runs on a background
	// goroutine (PrecompileBackend.executeAsync), same as it would in
	// production between the requesting tx and a later query tx.
	getResultInput := make([]byte, 1+32)
	getResultInput[0] = aiGetResult
	copy(getResultInput[1:33], requestID[:])

	deadline := time.Now().Add(5 * time.Second)
	var status uint8
	var outputCAS types.Hash
	for time.Now().Before(deadline) {
		out, err := precompile.Run(getResultInput)
		if err != nil {
			t.Fatalf("getResult Run: %v", err)
		}
		if len(out) != 64 {
			t.Fatalf("getResult output length = %d, want 64", len(out))
		}
		status = out[31]
		copy(outputCAS[:], out[32:64])
		if status == uint8(inference.RequestOptimisticVerified) || status == uint8(inference.RequestVerified) {
			break
		}
		if status == uint8(inference.RequestFailed) {
			t.Fatalf("inference request failed (status=%d)", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status != uint8(inference.RequestOptimisticVerified) && status != uint8(inference.RequestVerified) {
		t.Fatalf("timed out waiting for completion, last status=%d", status)
	}

	output, err := cas.Load(outputCAS)
	if err != nil {
		t.Fatalf("cas.Load output: %v", err)
	}
	got := binary.BigEndian.Uint64(output)
	if got != 720 {
		t.Fatalf("fac-ssa(6) via precompile = %d, want 720 (real WASM execution, not a stub)", got)
	}
}

// TestAIInferencePrecompileUnregisteredModelFailsClosed confirms the backend
// now surfaces an accurate "model not registered" error instead of the old
// blanket errAIInferenceNoBackend — proving getInferenceBackend() is non-nil
// (the actual P0 fix) while still correctly rejecting unregistered models.
func TestAIInferencePrecompileUnregisteredModelFailsClosed(t *testing.T) {
	ctx := context.Background()
	models := inference.NewModelRegistry()
	executor := inference.NewWazeroExecutor(ctx, inference.WazeroExecutorConfig{})
	defer executor.Close(ctx)
	svc := inference.NewInferenceService(models)
	svc.SetExecutor(executor)
	cas := inference.NewMemCAS()
	backend := inference.NewPrecompileBackend(svc, cas.Load, cas.Store)

	SetInferenceBackend(backend)
	defer SetInferenceBackend(nil)

	inputCAS, _ := cas.Store(i64BytesVM(1))
	reqInput := make([]byte, 1+65)
	reqInput[0] = aiRequestInference
	copy(reqInput[33:65], inputCAS[:]) // modelHash left zero: never registered

	precompile := &aiInference{}
	_, err := precompile.Run(reqInput)
	if err == nil {
		t.Fatal("expected an error for an unregistered model")
	}
	if err == errAIInferenceNoBackend {
		t.Fatal("backend should be set (not nil) — got the old no-backend error")
	}
}
