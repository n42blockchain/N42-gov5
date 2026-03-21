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

package zkprover

import (
	"errors"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func TestZKMLProver_GenerateCircuit(t *testing.T) {
	prover := NewZKMLProver()

	modelHash := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	weightsHash := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")

	circuit, err := prover.GenerateCircuit(modelHash, 4, weightsHash)
	if err != nil {
		t.Fatalf("GenerateCircuit: %v", err)
	}

	if circuit.ModelHash != modelHash {
		t.Fatalf("model hash mismatch")
	}
	if circuit.LayerCount != 4 {
		t.Fatalf("layer count = %d, want 4", circuit.LayerCount)
	}
	if circuit.Constraints != uint64(4)*constraintsPerLayer {
		t.Fatalf("constraints = %d, want %d", circuit.Constraints, uint64(4)*constraintsPerLayer)
	}
	if circuit.WeightHash != weightsHash {
		t.Fatalf("weights hash mismatch")
	}

	// Duplicate should fail.
	_, err = prover.GenerateCircuit(modelHash, 4, weightsHash)
	if !errors.Is(err, ErrCircuitAlreadyExists) {
		t.Fatalf("expected ErrCircuitAlreadyExists, got %v", err)
	}

	// Invalid layer count.
	_, err = prover.GenerateCircuit(types.HexToHash("0xAA"), 0, weightsHash)
	if !errors.Is(err, ErrInvalidLayerCount) {
		t.Fatalf("expected ErrInvalidLayerCount, got %v", err)
	}

	if prover.CircuitCount() != 1 {
		t.Fatalf("circuit count = %d, want 1", prover.CircuitCount())
	}
}

func TestZKMLProver_ProveInference(t *testing.T) {
	prover := NewZKMLProver()

	modelHash := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	weightsHash := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")

	prover.GenerateCircuit(modelHash, 3, weightsHash)

	// Create an execution trace.
	input := []byte("test input data")
	output := []byte("test output data")
	trace := NewExecutionTrace(modelHash, input)
	trace.AddLayer(0, "linear", 128, 64, []byte("layer0-input"), []byte("layer0-output"), []byte("layer0-weights"))
	trace.AddLayer(1, "relu", 64, 64, []byte("layer1-input"), []byte("layer1-output"), []byte("layer1-weights"))
	trace.AddLayer(2, "softmax", 64, 10, []byte("layer2-input"), []byte("layer2-output"), []byte("layer2-weights"))
	trace.Finalize()

	proof, err := prover.ProveInference(modelHash, input, output, trace)
	if err != nil {
		t.Fatalf("ProveInference: %v", err)
	}

	if proof.ModelHash != modelHash {
		t.Fatal("proof model hash mismatch")
	}
	if len(proof.ProofData) != 256 {
		t.Fatalf("proof data len = %d, want 256", len(proof.ProofData))
	}
	if len(proof.PublicInputs) != publicInputsSize {
		t.Fatalf("public inputs len = %d, want %d", len(proof.PublicInputs), publicInputsSize)
	}
	if proof.ProofType != ZKMLProofTypeGroth16 {
		t.Fatalf("proof type = %q, want %q", proof.ProofType, ZKMLProofTypeGroth16)
	}

	// No circuit for unknown model.
	_, err = prover.ProveInference(types.HexToHash("0xFF"), input, output, trace)
	if !errors.Is(err, ErrCircuitNotFound) {
		t.Fatalf("expected ErrCircuitNotFound, got %v", err)
	}

	// Empty input.
	_, err = prover.ProveInference(modelHash, nil, output, trace)
	if !errors.Is(err, ErrEmptyInput) {
		t.Fatalf("expected ErrEmptyInput, got %v", err)
	}

	// Empty output.
	_, err = prover.ProveInference(modelHash, input, nil, trace)
	if !errors.Is(err, ErrEmptyOutput) {
		t.Fatalf("expected ErrEmptyOutput, got %v", err)
	}

	// Nil trace.
	_, err = prover.ProveInference(modelHash, input, output, nil)
	if !errors.Is(err, ErrNilTrace) {
		t.Fatalf("expected ErrNilTrace, got %v", err)
	}
}

func TestZKMLProver_VerifyInference(t *testing.T) {
	prover := NewZKMLProver()

	modelHash := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	weightsHash := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")

	prover.GenerateCircuit(modelHash, 2, weightsHash)

	input := []byte("hello world")
	output := []byte("classification: cat")
	trace := NewExecutionTrace(modelHash, input)
	trace.AddLayer(0, "linear", 64, 32, []byte("i0"), []byte("o0"), []byte("w0"))
	trace.AddLayer(1, "softmax", 32, 10, []byte("i1"), []byte("o1"), []byte("w1"))
	trace.Finalize()

	proof, _ := prover.ProveInference(modelHash, input, output, trace)

	valid, err := prover.VerifyInference(proof)
	if err != nil {
		t.Fatalf("VerifyInference: %v", err)
	}
	if !valid {
		t.Fatal("expected valid proof")
	}

	// Nil proof.
	_, err = prover.VerifyInference(nil)
	if !errors.Is(err, ErrNilProof) {
		t.Fatalf("expected ErrNilProof, got %v", err)
	}

	// Empty proof data.
	badProof := *proof
	badProof.ProofData = nil
	_, err = prover.VerifyInference(&badProof)
	if !errors.Is(err, ErrProofDataEmpty) {
		t.Fatalf("expected ErrProofDataEmpty, got %v", err)
	}

	// Wrong public inputs length.
	badProof2 := *proof
	badProof2.PublicInputs = []byte("short")
	_, err = prover.VerifyInference(&badProof2)
	if !errors.Is(err, ErrPublicInputsLength) {
		t.Fatalf("expected ErrPublicInputsLength, got %v", err)
	}
}

func TestExecutionTrace(t *testing.T) {
	modelHash := types.HexToHash("0xABCD")
	input := []byte("trace input")

	trace := NewExecutionTrace(modelHash, input)
	trace.AddLayer(0, "linear", 128, 64, []byte("in0"), []byte("out0"), []byte("w0"))
	trace.AddLayer(1, "relu", 64, 64, []byte("in1"), []byte("out1"), []byte("w1"))

	if trace.LayerCount() != 2 {
		t.Fatalf("layer count = %d, want 2", trace.LayerCount())
	}

	// Duration should be 0 before finalization.
	if d := trace.Duration(); d != 0 {
		t.Fatalf("duration before finalize = %v, want 0", d)
	}

	// Set StartTime back so that duration is measurable.
	trace.StartTime = trace.StartTime.Add(-10 * time.Millisecond)
	trace.Finalize()

	if d := trace.Duration(); d <= 0 {
		t.Fatalf("duration after finalize = %v, want > 0", d)
	}

	// Serialize and deserialize.
	data, err := trace.Serialize()
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	restored, err := DeserializeTrace(data)
	if err != nil {
		t.Fatalf("DeserializeTrace: %v", err)
	}

	if restored.ModelHash != trace.ModelHash {
		t.Fatal("model hash mismatch after deserialization")
	}
	if restored.LayerCount() != trace.LayerCount() {
		t.Fatalf("layer count = %d, want %d", restored.LayerCount(), trace.LayerCount())
	}
	if restored.Layers[0].Type != "linear" {
		t.Fatalf("layer 0 type = %q, want %q", restored.Layers[0].Type, "linear")
	}
	if restored.Layers[1].Type != "relu" {
		t.Fatalf("layer 1 type = %q, want %q", restored.Layers[1].Type, "relu")
	}

	// Validate.
	if err := restored.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	// Validate empty trace.
	emptyTrace := &ExecutionTrace{}
	if err := emptyTrace.Validate(); !errors.Is(err, ErrTraceNoLayers) {
		t.Fatalf("expected ErrTraceNoLayers, got %v", err)
	}
}

func TestZKMLProver_CircuitCount(t *testing.T) {
	prover := NewZKMLProver()

	if prover.CircuitCount() != 0 {
		t.Fatalf("initial circuit count = %d, want 0", prover.CircuitCount())
	}

	weightsHash := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")

	prover.GenerateCircuit(types.HexToHash("0xAA"), 2, weightsHash)
	if prover.CircuitCount() != 1 {
		t.Fatalf("circuit count after 1 = %d, want 1", prover.CircuitCount())
	}

	prover.GenerateCircuit(types.HexToHash("0xBB"), 3, weightsHash)
	if prover.CircuitCount() != 2 {
		t.Fatalf("circuit count after 2 = %d, want 2", prover.CircuitCount())
	}

	prover.GenerateCircuit(types.HexToHash("0xCC"), 1, weightsHash)
	if prover.CircuitCount() != 3 {
		t.Fatalf("circuit count after 3 = %d, want 3", prover.CircuitCount())
	}
}

func TestZKMLProver_GetCircuitNotFound(t *testing.T) {
	prover := NewZKMLProver()

	nonExistent := types.HexToHash("0xDEADBEEF")
	_, err := prover.GetCircuit(nonExistent)
	if err == nil {
		t.Fatal("expected error for non-existent circuit")
	}
	if !errors.Is(err, ErrCircuitNotFound) {
		t.Fatalf("expected ErrCircuitNotFound, got %v", err)
	}
}

func TestZKMLProver_ProveWithoutCircuit(t *testing.T) {
	prover := NewZKMLProver()

	modelHash := types.HexToHash("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	input := []byte("test input")
	output := []byte("test output")

	trace := NewExecutionTrace(modelHash, input)
	trace.AddLayer(0, "linear", 64, 32, []byte("in"), []byte("out"), []byte("w"))
	trace.Finalize()

	_, err := prover.ProveInference(modelHash, input, output, trace)
	if err == nil {
		t.Fatal("expected error when proving without circuit")
	}
	if !errors.Is(err, ErrCircuitNotFound) {
		t.Fatalf("expected ErrCircuitNotFound, got %v", err)
	}
}

func TestExecutionTrace_ValidateEmpty(t *testing.T) {
	trace := &ExecutionTrace{}

	err := trace.Validate()
	if err == nil {
		t.Fatal("expected error for trace with no layers")
	}
	if !errors.Is(err, ErrTraceNoLayers) {
		t.Fatalf("expected ErrTraceNoLayers, got %v", err)
	}
}

func TestExecutionTrace_Duration(t *testing.T) {
	modelHash := types.HexToHash("0xABCD")
	input := []byte("duration test input")

	trace := NewExecutionTrace(modelHash, input)

	// Before finalization, duration should be zero.
	if d := trace.Duration(); d != 0 {
		t.Fatalf("duration before finalize = %v, want 0", d)
	}

	// Set start time back to create a measurable duration.
	trace.StartTime = trace.StartTime.Add(-50 * time.Millisecond)
	trace.Finalize()

	d := trace.Duration()
	if d <= 0 {
		t.Fatalf("duration after finalize = %v, want > 0", d)
	}
	// Duration should be at least 50ms since we moved start time back.
	if d < 50*time.Millisecond {
		t.Fatalf("duration = %v, want >= 50ms", d)
	}
}
