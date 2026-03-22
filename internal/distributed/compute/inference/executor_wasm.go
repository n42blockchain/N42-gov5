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

package inference

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
)

// Default limits for the WASM executor.
const (
	// DefaultMaxModelSize is the default maximum WASM model size (64 MiB).
	DefaultMaxModelSize int64 = 64 << 20

	// DefaultFuelLimit is the default fuel (gas) limit for WASM execution.
	DefaultFuelLimit uint64 = 10_000_000
)

// WASMExecutor implements InferenceExecutor using cached WASM model bytecode.
//
// Models are loaded into memory as raw WASM bytes. The Execute method looks up
// the model by hash, runs a simulated execution (producing a deterministic
// output hash), and returns an InferenceResult.
//
// In production, this would delegate to the wasm.Engine for actual WASM
// execution. The current implementation provides the interface contract and
// deterministic hashing for integration with the precompile and inference service.
type WASMExecutor struct {
	mu           sync.RWMutex
	modelCache   map[types.Hash][]byte // cached WASM module bytecode
	maxModelSize int64
	fuelLimit    uint64
}

// NewWASMExecutor creates a new WASM-based inference executor.
//
// maxModelSize limits the byte size of WASM modules that can be loaded.
// fuelLimit sets the maximum fuel (gas) available per execution.
func NewWASMExecutor(maxModelSize int64, fuelLimit uint64) *WASMExecutor {
	if maxModelSize <= 0 {
		maxModelSize = DefaultMaxModelSize
	}
	if fuelLimit == 0 {
		fuelLimit = DefaultFuelLimit
	}
	return &WASMExecutor{
		modelCache:   make(map[types.Hash][]byte),
		maxModelSize: maxModelSize,
		fuelLimit:    fuelLimit,
	}
}

// LoadModel validates and caches a WASM module for inference execution.
// The modelHash is the content-addressed identity of the model.
// Returns an error if the module exceeds the size limit or is empty.
func (e *WASMExecutor) LoadModel(modelHash types.Hash, wasmBytes []byte) error {
	if len(wasmBytes) == 0 {
		return fmt.Errorf("wasm executor: empty model bytecode for %s", modelHash.Hex())
	}
	if int64(len(wasmBytes)) > e.maxModelSize {
		return fmt.Errorf("wasm executor: model %s size %d exceeds limit %d",
			modelHash.Hex(), len(wasmBytes), e.maxModelSize)
	}

	// Validate WASM magic number (\0asm).
	if len(wasmBytes) < 4 || wasmBytes[0] != 0x00 || wasmBytes[1] != 0x61 ||
		wasmBytes[2] != 0x73 || wasmBytes[3] != 0x6d {
		return fmt.Errorf("wasm executor: invalid WASM magic number for model %s", modelHash.Hex())
	}

	// Make a defensive copy to prevent external mutation.
	moduleCopy := make([]byte, len(wasmBytes))
	copy(moduleCopy, wasmBytes)

	e.mu.Lock()
	defer e.mu.Unlock()
	e.modelCache[modelHash] = moduleCopy

	return nil
}

// UnloadModel removes a cached WASM model from memory.
func (e *WASMExecutor) UnloadModel(modelHash types.Hash) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.modelCache, modelHash)
}

// LoadedModels returns the hashes of all currently loaded WASM models.
func (e *WASMExecutor) LoadedModels() []types.Hash {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if len(e.modelCache) == 0 {
		return nil
	}
	hashes := make([]types.Hash, 0, len(e.modelCache))
	for h := range e.modelCache {
		hashes = append(hashes, h)
	}
	return hashes
}

// Execute runs inference using a cached WASM model.
//
// The execution flow:
//  1. Look up the model in the cache by modelHash.
//  2. If not found, return an error (model must be pre-loaded via LoadModel).
//  3. Compute a deterministic request ID from modelHash + input hash.
//  4. Execute the model with the input data (fuel-metered).
//  5. Compute the output hash via Keccak256.
//  6. Return an InferenceResult with the output, confidence, and timing.
//
// The ctx parameter supports cancellation and deadline propagation.
func (e *WASMExecutor) Execute(ctx context.Context, modelHash types.Hash, input []byte) (*InferenceResult, error) {
	start := time.Now()

	// Check for context cancellation before proceeding.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Look up the model in the cache.
	e.mu.RLock()
	wasmBytes, ok := e.modelCache[modelHash]
	if !ok {
		e.mu.RUnlock()
		return nil, fmt.Errorf("wasm executor: model %s not loaded", modelHash.Hex())
	}
	// Take a reference to the byte slice — safe because LoadModel stores a copy
	// and the map entry is never mutated in place.
	_ = wasmBytes
	e.mu.RUnlock()

	// Compute deterministic request ID from model hash and input hash.
	inputHash := crypto.Keccak256Hash(input)
	requestIDData := make([]byte, 64)
	copy(requestIDData[0:32], modelHash[:])
	copy(requestIDData[32:64], inputHash[:])
	requestID := crypto.Keccak256Hash(requestIDData)

	// Execute the WASM model.
	// In a full implementation, this would invoke the wasm.Engine to run the
	// compiled module with fuel metering. Here we produce a deterministic
	// output derived from the model and input for correctness of the pipeline.
	output, gasUsed, err := e.executeWASM(ctx, modelHash, wasmBytes, input)
	if err != nil {
		return nil, fmt.Errorf("wasm executor: execution failed for model %s: %w", modelHash.Hex(), err)
	}

	executionTime := time.Since(start)

	return &InferenceResult{
		RequestID:    requestID,
		Output:       output,
		Confidence:   1.0, // WASM execution is deterministic
		ModelVersion: "wasm-v1",
		GasUsed:      gasUsed,
		ComputedAt:   time.Now().Add(-executionTime), // align with start time
	}, nil
}

// executeWASM runs the WASM module with fuel metering.
//
// This is the integration point for the wasm.Engine. The current implementation
// produces a deterministic output hash from the model bytes and input, suitable
// for testing and precompile integration while the full WASM runtime is wired in.
func (e *WASMExecutor) executeWASM(ctx context.Context, modelHash types.Hash, wasmBytes []byte, input []byte) ([]byte, uint64, error) {
	// Check context deadline/cancellation.
	select {
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	default:
	}

	// Simulate fuel consumption based on input size.
	// Base cost + per-byte cost for both model reference and input processing.
	baseFuel := uint64(1000)
	inputFuel := uint64(len(input)) * 10
	totalFuel := baseFuel + inputFuel

	if totalFuel > e.fuelLimit {
		return nil, 0, fmt.Errorf("wasm executor: fuel limit exceeded (%d > %d)", totalFuel, e.fuelLimit)
	}

	// Produce deterministic output: Keccak256(modelHash || input).
	// In a real implementation, the WASM runtime would execute the model's
	// exported function and return its output bytes.
	hashInput := make([]byte, 32+len(input))
	copy(hashInput[0:32], modelHash[:])
	copy(hashInput[32:], input)
	outputHash := crypto.Keccak256Hash(hashInput)

	return outputHash[:], totalFuel, nil
}
