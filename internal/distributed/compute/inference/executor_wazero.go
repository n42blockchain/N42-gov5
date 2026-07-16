// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// WazeroExecutor implements InferenceExecutor by running the model as real
// WASM bytecode through wasm.Engine, backed by wasm.NewWazeroRuntime. Unlike
// WASMExecutor (executor_wasm_stub.go — renamed from executor_wasm.go, whose
// original name was silently excluded from every non-wasm build by Go's
// implicit _GOARCH.go build-constraint convention; see the rename commit),
// which the package's own comment already documents as a placeholder that
// hashes model+input together instead of executing anything, this executor
// genuinely compiles and instantiates the guest module and calls its
// exported function — see internal/distributed/compute/wasm/wazero_runtime.go
// for the calling convention (single i64 scalar, or byte-blob via linear memory).

package inference

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/distributed/compute/wasm"
)

// WazeroExecutorConfig parameterizes real WASM execution.
type WazeroExecutorConfig struct {
	// FuncName is the guest export invoked for every inference. Defaults to "infer".
	FuncName string
	// FuelLimit bounds each execution (see wasm.wazeroCompiledModule.Execute —
	// this is wall-clock-derived, not per-instruction, since wazero has no
	// native fuel counter).
	FuelLimit uint64
	// MaxMemoryMB / MaxExecTimeSec / GasMultiplier configure the underlying wasm.Engine.
	MaxMemoryMB    int
	MaxExecTimeSec int
	GasMultiplier  float64
}

// WazeroExecutor runs inference requests as real WASM execution via
// wasm.Engine. Models must be loaded (LoadModel) before use, same discipline
// as WASMExecutor and SchedulerInferenceExecutor.
type WazeroExecutor struct {
	engine *wasm.Engine
	cfg    WazeroExecutorConfig

	mu         sync.RWMutex
	modelCache map[types.Hash][]byte
}

// NewWazeroExecutor creates an executor backed by a real wazero runtime.
// The caller owns the returned executor's lifetime; call Close to release the
// underlying wazero runtime.
func NewWazeroExecutor(ctx context.Context, cfg WazeroExecutorConfig) *WazeroExecutor {
	if cfg.FuncName == "" {
		cfg.FuncName = "infer"
	}
	if cfg.FuelLimit == 0 {
		cfg.FuelLimit = DefaultFuelLimit
	}
	if cfg.MaxMemoryMB == 0 {
		cfg.MaxMemoryMB = 64
	}
	if cfg.MaxExecTimeSec == 0 {
		cfg.MaxExecTimeSec = 30
	}
	if cfg.GasMultiplier == 0 {
		cfg.GasMultiplier = 1.0
	}
	rt := wasm.NewWazeroRuntime(ctx)
	engine := wasm.NewEngine(rt, cfg.MaxMemoryMB, cfg.MaxExecTimeSec, cfg.GasMultiplier)
	return &WazeroExecutor{
		engine:     engine,
		cfg:        cfg,
		modelCache: make(map[types.Hash][]byte),
	}
}

// LoadModel caches a model's WASM bytecode so Execute can run it by hash.
func (e *WazeroExecutor) LoadModel(modelHash types.Hash, wasmBytes []byte) error {
	if len(wasmBytes) < 4 || wasmBytes[0] != 0x00 || wasmBytes[1] != 0x61 ||
		wasmBytes[2] != 0x73 || wasmBytes[3] != 0x6d {
		return fmt.Errorf("wazero executor: invalid WASM magic number for model %s", modelHash.Hex())
	}
	cp := make([]byte, len(wasmBytes))
	copy(cp, wasmBytes)
	e.mu.Lock()
	e.modelCache[modelHash] = cp
	e.mu.Unlock()
	return nil
}

// UnloadModel drops a cached model and its compiled form from the engine.
func (e *WazeroExecutor) UnloadModel(modelHash types.Hash) {
	e.mu.Lock()
	delete(e.modelCache, modelHash)
	e.mu.Unlock()
	e.engine.Invalidate(modelHash)
}

// Execute satisfies InferenceExecutor: runs the cached model's WASM export
// with input, via the real wazero-backed engine.
func (e *WazeroExecutor) Execute(ctx context.Context, modelHash types.Hash, input []byte) (*InferenceResult, error) {
	e.mu.RLock()
	wasmBytes, ok := e.modelCache[modelHash]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("wazero executor: model %s not loaded", modelHash.Hex())
	}

	start := time.Now()
	result, err := e.engine.Execute(ctx, modelHash, wasmBytes, e.cfg.FuncName, input, e.cfg.FuelLimit)
	if err != nil {
		return nil, fmt.Errorf("wazero executor: %w", err)
	}

	return &InferenceResult{
		Output:       result.Output,
		Confidence:   1.0, // deterministic guest execution for a given (module, input)
		ModelVersion: "wazero-v1",
		GasUsed:      result.GasUsed,
		ComputedAt:   start,
	}, nil
}

// Close releases the underlying wazero runtime and all compiled modules.
func (e *WazeroExecutor) Close(ctx context.Context) error {
	return e.engine.Close(ctx)
}
