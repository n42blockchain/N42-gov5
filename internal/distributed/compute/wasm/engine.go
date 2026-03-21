// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package wasm provides a sandboxed WASM execution engine for distributed
// compute tasks. Designed for integration with wazero (pure Go, no CGO).
//
// Architecture:
//   - Runtime interface abstracts the WASM runtime (wazero)
//   - Engine manages compilation caching, gas metering, and sandboxing
//   - Host functions provide CAS storage, cryptographic primitives, and logging
//   - Deterministic execution: no filesystem, no network, no randomness
package wasm

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// Runtime abstracts the WASM execution environment.
// Production implementation wraps wazero's runtime.
type Runtime interface {
	// Compile compiles WASM bytecode into an executable module.
	Compile(ctx context.Context, wasm []byte) (CompiledModule, error)

	// Close releases all runtime resources.
	Close(ctx context.Context) error
}

// CompiledModule represents a compiled WASM module ready for execution.
type CompiledModule interface {
	// Execute runs the named function with the given arguments and fuel limit.
	// Returns output bytes, fuel consumed, and any error.
	Execute(ctx context.Context, funcName string, args []byte, fuel uint64) ([]byte, uint64, error)

	// Close releases module resources.
	Close(ctx context.Context) error
}

// ExecutionResult holds the output of a WASM execution.
type ExecutionResult struct {
	Output   []byte     `json:"output"`
	GasUsed  uint64     `json:"gasUsed"`
	Duration time.Duration `json:"duration"`
}

// Engine manages WASM module compilation, caching, and execution with
// gas metering and resource limits.
type Engine struct {
	mu            sync.RWMutex
	runtime       Runtime
	cache         map[types.Hash]CompiledModule // bytecodeHash → compiled
	gasTable      *GasTable
	maxMemoryMB   int
	maxExecTime   time.Duration
	gasMultiplier float64
	hostFuncs     *HostFunctions
}

// NewEngine creates a WASM execution engine with the given configuration.
func NewEngine(rt Runtime, maxMemoryMB int, maxExecTimeSec int, gasMultiplier float64) *Engine {
	return &Engine{
		runtime:       rt,
		cache:         make(map[types.Hash]CompiledModule),
		gasTable:      DefaultGasTable(),
		maxMemoryMB:   maxMemoryMB,
		maxExecTime:   time.Duration(maxExecTimeSec) * time.Second,
		gasMultiplier: gasMultiplier,
		hostFuncs:     NewHostFunctions(),
	}
}

// Execute compiles (or retrieves from cache) and runs a WASM module.
func (e *Engine) Execute(ctx context.Context, bytecodeHash types.Hash, bytecode []byte, funcName string, args []byte, gasLimit uint64) (*ExecutionResult, error) {
	if e.runtime == nil {
		return nil, fmt.Errorf("wasm: runtime not initialized")
	}

	start := time.Now()

	// Apply execution timeout
	execCtx, cancel := context.WithTimeout(ctx, e.maxExecTime)
	defer cancel()

	// Get or compile module
	compiled, err := e.getOrCompile(execCtx, bytecodeHash, bytecode)
	if err != nil {
		return nil, fmt.Errorf("wasm: compilation failed: %w", err)
	}

	// Apply gas multiplier using integer math with 1000x precision to avoid
	// floating-point rounding errors in gas accounting.
	multiplierX1000 := uint64(e.gasMultiplier * 1000)
	if multiplierX1000 == 0 {
		multiplierX1000 = 1000
	}
	adjustedGas := gasLimit * 1000 / multiplierX1000
	if adjustedGas == 0 {
		adjustedGas = 1
	}

	// Execute
	output, fuelUsed, err := compiled.Execute(execCtx, funcName, args, adjustedGas)
	if err != nil {
		return nil, fmt.Errorf("wasm: execution failed: %w", err)
	}

	// Convert fuel back to gas using same precision factor
	gasUsed := fuelUsed * multiplierX1000 / 1000

	return &ExecutionResult{
		Output:   output,
		GasUsed:  gasUsed,
		Duration: time.Since(start),
	}, nil
}

// getOrCompile returns a cached compiled module or compiles a new one.
func (e *Engine) getOrCompile(ctx context.Context, hash types.Hash, bytecode []byte) (CompiledModule, error) {
	e.mu.RLock()
	if cached, ok := e.cache[hash]; ok {
		e.mu.RUnlock()
		return cached, nil
	}
	e.mu.RUnlock()

	e.mu.Lock()
	defer e.mu.Unlock()

	// Double-check after acquiring write lock
	if cached, ok := e.cache[hash]; ok {
		return cached, nil
	}

	compiled, err := e.runtime.Compile(ctx, bytecode)
	if err != nil {
		return nil, err
	}
	e.cache[hash] = compiled
	return compiled, nil
}

// Invalidate removes a compiled module from the cache.
func (e *Engine) Invalidate(hash types.Hash) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if compiled, ok := e.cache[hash]; ok {
		compiled.Close(context.Background())
		delete(e.cache, hash)
	}
}

// CacheSize returns the number of cached compiled modules.
func (e *Engine) CacheSize() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.cache)
}

// GasTable returns the engine's gas pricing table.
func (e *Engine) GasTable() *GasTable {
	return e.gasTable
}

// HostFunctions returns the engine's host function registry.
func (e *Engine) HostFunctions() *HostFunctions {
	return e.hostFuncs
}

// Stats returns engine statistics.
func (e *Engine) Stats() map[string]interface{} {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return map[string]interface{}{
		"cacheSize":     len(e.cache),
		"maxMemoryMB":   e.maxMemoryMB,
		"gasMultiplier": e.gasMultiplier,
	}
}

// Close releases all engine resources including cached modules.
func (e *Engine) Close(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for hash, compiled := range e.cache {
		compiled.Close(ctx)
		delete(e.cache, hash)
	}
	if e.runtime != nil {
		return e.runtime.Close(ctx)
	}
	return nil
}
