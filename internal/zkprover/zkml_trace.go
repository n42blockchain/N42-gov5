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
//
// ExecutionTrace capture for ZKML. Records per-layer intermediate
// activation tensors during model inference so the ZKMLProver can
// build a witness that proves the model output matches the public
// modelHash and inputHash. JSON-serialisable for storage in the
// coprocessor task store.

package zkprover

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
)

var (
	// ErrTraceNoLayers is returned when a trace has no layers.
	ErrTraceNoLayers = errors.New("zkml trace: no layers recorded")

	// ErrTraceZeroHash is returned when a layer contains a zero hash.
	ErrTraceZeroHash = errors.New("zkml trace: layer contains zero hash")

	// ErrTraceInvalidTime is returned when StartTime is not before EndTime.
	ErrTraceInvalidTime = errors.New("zkml trace: start time must be before end time")

	// ErrTraceNotFinalized is returned when a trace has not been finalized.
	ErrTraceNotFinalized = errors.New("zkml trace: trace not finalized")
)

// ExecutionTrace captures the intermediate values during ML inference for ZK
// proving. Each layer's inputs, outputs, and weights are hashed to form the
// witness that the ZK circuit operates on. The trace allows the prover to
// reconstruct the full computation path without storing raw tensor data.
type ExecutionTrace struct {
	ModelHash types.Hash   `json:"modelHash"`
	InputHash types.Hash   `json:"inputHash"`
	Layers    []LayerTrace `json:"layers"`
	StartTime time.Time    `json:"startTime"`
	EndTime   time.Time    `json:"endTime"`
}

// LayerTrace captures one layer's computation within the inference pipeline.
// Each layer records its type (e.g., linear, relu, conv2d) along with hashes
// of its inputs, outputs, and weights. These hashes form the witness that
// the ZK circuit verifies for computational integrity.
type LayerTrace struct {
	Index      int        `json:"index"`
	Type       string     `json:"type"` // "linear", "relu", "softmax", "conv2d"
	InputSize  int        `json:"inputSize"`
	OutputSize int        `json:"outputSize"`
	InputHash  types.Hash `json:"inputHash"`
	OutputHash types.Hash `json:"outputHash"`
	WeightHash types.Hash `json:"weightHash"`
}

// NewExecutionTrace creates a new ExecutionTrace for the given model and input.
// The input is immediately hashed via Keccak256 and the start time is recorded.
// Callers should add layers via AddLayer and then call Finalize when inference
// is complete.
func NewExecutionTrace(modelHash types.Hash, input []byte) *ExecutionTrace {
	return &ExecutionTrace{
		ModelHash: modelHash,
		InputHash: crypto.Keccak256Hash(input),
		Layers:    make([]LayerTrace, 0),
		StartTime: time.Now(),
	}
}

// AddLayer records a layer's computation in the trace. The raw input, output,
// and weight tensors are hashed via Keccak256 to produce compact commitments.
// Layers should be added in execution order (index 0, 1, 2, ...).
func (t *ExecutionTrace) AddLayer(index int, layerType string, inputSize, outputSize int, input, output, weights []byte) {
	layer := LayerTrace{
		Index:      index,
		Type:       layerType,
		InputSize:  inputSize,
		OutputSize: outputSize,
		InputHash:  crypto.Keccak256Hash(input),
		OutputHash: crypto.Keccak256Hash(output),
		WeightHash: crypto.Keccak256Hash(weights),
	}
	t.Layers = append(t.Layers, layer)
}

// Finalize marks the trace as complete by recording the end time.
// Must be called after all layers have been added and before the trace
// is used for proof generation.
func (t *ExecutionTrace) Finalize() {
	t.EndTime = time.Now()
}

// Serialize marshals the execution trace to JSON bytes.
func (t *ExecutionTrace) Serialize() ([]byte, error) {
	return json.Marshal(t)
}

// DeserializeTrace unmarshals an execution trace from JSON bytes.
func DeserializeTrace(data []byte) (*ExecutionTrace, error) {
	var trace ExecutionTrace
	if err := json.Unmarshal(data, &trace); err != nil {
		return nil, fmt.Errorf("zkml trace: failed to deserialize: %w", err)
	}
	return &trace, nil
}

// LayerCount returns the number of layers recorded in the trace.
func (t *ExecutionTrace) LayerCount() int {
	return len(t.Layers)
}

// Duration returns the wall-clock duration of the inference execution.
// Returns zero if the trace has not been finalized.
func (t *ExecutionTrace) Duration() time.Duration {
	if t.EndTime.IsZero() {
		return 0
	}
	return t.EndTime.Sub(t.StartTime)
}

// Validate checks that the execution trace is well-formed:
//   - At least one layer must be recorded.
//   - All layer hashes (input, output, weight) must be non-zero.
//   - The trace must be finalized (EndTime set).
//   - StartTime must be before EndTime.
func (t *ExecutionTrace) Validate() error {
	if len(t.Layers) == 0 {
		return ErrTraceNoLayers
	}

	if t.EndTime.IsZero() {
		return ErrTraceNotFinalized
	}

	if !t.StartTime.Before(t.EndTime) {
		return ErrTraceInvalidTime
	}

	var zeroHash types.Hash
	for i, layer := range t.Layers {
		if layer.InputHash == zeroHash {
			return fmt.Errorf("%w: layer %d input hash", ErrTraceZeroHash, i)
		}
		if layer.OutputHash == zeroHash {
			return fmt.Errorf("%w: layer %d output hash", ErrTraceZeroHash, i)
		}
		if layer.WeightHash == zeroHash {
			return fmt.Errorf("%w: layer %d weight hash", ErrTraceZeroHash, i)
		}
	}

	return nil
}
