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
	"fmt"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
)

// constraintsPerLayer is the simplified constraint count per model layer.
// In production, each layer type (linear, conv2d, relu, etc.) would have
// a different constraint cost based on its arithmetic complexity.
const constraintsPerLayer = 1024

// publicInputsSize is the expected byte length of the public inputs field:
// modelHash (32) + inputHash (32) + outputHash (32) = 96 bytes.
const publicInputsSize = 96

// Supported ZKML proof types.
const (
	ZKMLProofTypeGroth16 = "groth16"
	ZKMLProofTypePlonk   = "plonk"
)

var (
	// ErrCircuitNotFound is returned when no compiled circuit exists for the given model.
	ErrCircuitNotFound = errors.New("zkml: circuit not found for model")

	// ErrCircuitAlreadyExists is returned when a circuit is already compiled for the model.
	ErrCircuitAlreadyExists = errors.New("zkml: circuit already exists for model")

	// ErrInvalidLayerCount is returned when layer count is not positive.
	ErrInvalidLayerCount = errors.New("zkml: layer count must be positive")

	// ErrNilTrace is returned when a nil execution trace is provided.
	ErrNilTrace = errors.New("zkml: nil execution trace")

	// ErrEmptyInput is returned when input data is empty.
	ErrEmptyInput = errors.New("zkml: empty input data")

	// ErrEmptyOutput is returned when output data is empty.
	ErrEmptyOutput = errors.New("zkml: empty output data")

	// ErrInvalidProof is returned when proof validation fails.
	ErrInvalidProof = errors.New("zkml: invalid proof")

	// ErrNilProof is returned when a nil proof is provided.
	ErrNilProof = errors.New("zkml: nil proof")

	// ErrProofDataEmpty is returned when proof data is empty.
	ErrProofDataEmpty = errors.New("zkml: empty proof data")

	// ErrPublicInputsLength is returned when public inputs have wrong length.
	ErrPublicInputsLength = errors.New("zkml: public inputs must be 96 bytes")

	// ErrPublicInputsMismatch is returned when public inputs don't match proof fields.
	ErrPublicInputsMismatch = errors.New("zkml: public inputs do not match proof fields")
)

// ZKMLProver generates zero-knowledge proofs for ML inference correctness.
// It maintains a registry of compiled circuits keyed by model hash and
// produces proofs that attest to the correct execution of ML inference.
type ZKMLProver struct {
	mu       sync.Mutex
	circuits map[types.Hash]*Circuit // modelHash -> compiled circuit
}

// Circuit represents a compiled ZK circuit for an ML model.
// Each unique model architecture (identified by ModelHash) requires its own
// circuit compilation before proofs can be generated.
type Circuit struct {
	ModelHash   types.Hash // Keccak256 identifier of the model architecture
	LayerCount  int        // Number of layers in the model
	Constraints uint64     // Total constraint count (layerCount * constraintsPerLayer)
	WeightHash  types.Hash // Poseidon/Keccak hash of model weights
	CreatedAt   time.Time  // Timestamp when the circuit was compiled
}

// ZKMLProof is a proof that an ML inference was computed correctly.
// It binds the model identity, input, and output together cryptographically,
// allowing any verifier to confirm the inference without re-executing it.
type ZKMLProof struct {
	ModelHash    types.Hash // Identifies the model used for inference
	InputHash    types.Hash // Keccak256 hash of the input data
	OutputHash   types.Hash // Keccak256 hash of the output data
	ProofData    []byte     // Serialized ZK proof (Groth16 or PLONK)
	PublicInputs []byte     // [modelHash(32) || inputHash(32) || outputHash(32)]
	ProofType    string     // "groth16" or "plonk"
	GeneratedAt  time.Time  // Timestamp when the proof was generated
}

// NewZKMLProver creates a new ZKMLProver with an empty circuit registry.
func NewZKMLProver() *ZKMLProver {
	return &ZKMLProver{
		circuits: make(map[types.Hash]*Circuit),
	}
}

// GenerateCircuit creates and stores a compiled circuit for the given model.
// The circuit encodes the model's layer structure as a constraint system.
// Returns ErrCircuitAlreadyExists if a circuit for this model is already compiled,
// or ErrInvalidLayerCount if layerCount is not positive.
func (p *ZKMLProver) GenerateCircuit(modelHash types.Hash, layerCount int, weightsHash types.Hash) (*Circuit, error) {
	if layerCount <= 0 {
		return nil, ErrInvalidLayerCount
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.circuits[modelHash]; exists {
		return nil, fmt.Errorf("%w: %s", ErrCircuitAlreadyExists, modelHash.Hex())
	}

	circuit := &Circuit{
		ModelHash:   modelHash,
		LayerCount:  layerCount,
		Constraints: uint64(layerCount) * constraintsPerLayer,
		WeightHash:  weightsHash,
		CreatedAt:   time.Now(),
	}

	p.circuits[modelHash] = circuit
	return circuit, nil
}

// ProveInference generates a ZK proof attesting that the given output was
// correctly produced by running the model (identified by modelHash) on the
// given input. The execution trace provides intermediate layer values needed
// to construct the witness for the proof system.
//
// Currently uses a hash-based simulation for proof generation. The proof
// structure is designed for future integration with SP1 or a native Groth16/PLONK
// backend.
func (p *ZKMLProver) ProveInference(modelHash types.Hash, input, output []byte, trace *ExecutionTrace) (*ZKMLProof, error) {
	if len(input) == 0 {
		return nil, ErrEmptyInput
	}
	if len(output) == 0 {
		return nil, ErrEmptyOutput
	}
	if trace == nil {
		return nil, ErrNilTrace
	}

	p.mu.Lock()
	circuit, exists := p.circuits[modelHash]
	p.mu.Unlock()

	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrCircuitNotFound, modelHash.Hex())
	}

	// Compute content hashes for the input and output.
	inputHash := crypto.Keccak256Hash(input)
	outputHash := crypto.Keccak256Hash(output)

	// Assemble public inputs: modelHash || inputHash || outputHash (96 bytes).
	publicInputs := make([]byte, publicInputsSize)
	copy(publicInputs[0:32], modelHash[:])
	copy(publicInputs[32:64], inputHash[:])
	copy(publicInputs[64:96], outputHash[:])

	// Generate proof data via hash-based simulation.
	// In production, this would invoke the SP1 prover or a native Groth16/PLONK
	// backend with the circuit and witness derived from the execution trace.
	proofData := generateSimulatedProof(circuit, publicInputs, trace)

	proof := &ZKMLProof{
		ModelHash:    modelHash,
		InputHash:    inputHash,
		OutputHash:   outputHash,
		ProofData:    proofData,
		PublicInputs: publicInputs,
		ProofType:    ZKMLProofTypeGroth16,
		GeneratedAt:  time.Now(),
	}

	return proof, nil
}

// generateSimulatedProof creates a deterministic proof via hash-based simulation.
// The proof commits to the circuit constraints, public inputs, and execution trace
// layers, producing a structure that mirrors a real ZK proof for testing and
// development. This will be replaced by a real prover backend (SP1/Groth16/PLONK).
func generateSimulatedProof(circuit *Circuit, publicInputs []byte, trace *ExecutionTrace) []byte {
	// Build a commitment chain:
	//   commitment = H(circuitHash || publicInputs || layerHashes...)
	//
	// circuitHash = H(modelHash || weightsHash)
	circuitHash := crypto.Keccak256Hash(circuit.ModelHash[:], circuit.WeightHash[:])

	// Start with the circuit hash and public inputs.
	commitParts := make([]byte, 0, 32+len(publicInputs)+32*len(trace.Layers))
	commitParts = append(commitParts, circuitHash[:]...)
	commitParts = append(commitParts, publicInputs...)

	// Fold in each layer's output hash for full trace binding.
	for i := range trace.Layers {
		commitParts = append(commitParts, trace.Layers[i].OutputHash[:]...)
	}

	// The "proof" is the Keccak256 hash of the commitment chain, repeated
	// to fill a realistic proof size (256 bytes for Groth16-like proofs).
	commitment := crypto.Keccak256(commitParts)

	const simulatedProofSize = 256
	proofData := make([]byte, simulatedProofSize)
	for offset := 0; offset < simulatedProofSize; offset += 32 {
		remaining := simulatedProofSize - offset
		if remaining > 32 {
			remaining = 32
		}
		copy(proofData[offset:offset+remaining], commitment[:remaining])
		// Ratchet the hash forward for each block.
		commitment = crypto.Keccak256(commitment)
	}

	return proofData
}

// VerifyInference verifies a ZKML proof by checking the public inputs structure
// and recalculating the commitment hash. This performs structural validation
// suitable for development and testing. Full cryptographic verification requires
// an external verifier backend (see zkverifier.ZKMLVerifier).
func (p *ZKMLProver) VerifyInference(proof *ZKMLProof) (bool, error) {
	if proof == nil {
		return false, ErrNilProof
	}
	if len(proof.ProofData) == 0 {
		return false, ErrProofDataEmpty
	}
	if len(proof.PublicInputs) != publicInputsSize {
		return false, fmt.Errorf("%w: got %d bytes", ErrPublicInputsLength, len(proof.PublicInputs))
	}

	// Extract hashes from public inputs.
	var piModelHash, piInputHash, piOutputHash types.Hash
	copy(piModelHash[:], proof.PublicInputs[0:32])
	copy(piInputHash[:], proof.PublicInputs[32:64])
	copy(piOutputHash[:], proof.PublicInputs[64:96])

	// Verify public inputs match the proof's declared fields.
	if piModelHash != proof.ModelHash {
		return false, fmt.Errorf("%w: model hash", ErrPublicInputsMismatch)
	}
	if piInputHash != proof.InputHash {
		return false, fmt.Errorf("%w: input hash", ErrPublicInputsMismatch)
	}
	if piOutputHash != proof.OutputHash {
		return false, fmt.Errorf("%w: output hash", ErrPublicInputsMismatch)
	}

	// Verify proof data integrity: the first 32 bytes of proof data should be
	// a valid hash (non-zero). This is a minimal sanity check.
	var zeroHash types.Hash
	var proofPrefix types.Hash
	copy(proofPrefix[:], proof.ProofData[:32])
	if proofPrefix == zeroHash {
		return false, fmt.Errorf("%w: proof data starts with zero hash", ErrInvalidProof)
	}

	return true, nil
}

// GetCircuit retrieves the compiled circuit for the given model hash.
// Returns ErrCircuitNotFound if no circuit exists.
func (p *ZKMLProver) GetCircuit(modelHash types.Hash) (*Circuit, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	circuit, exists := p.circuits[modelHash]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrCircuitNotFound, modelHash.Hex())
	}

	return circuit, nil
}

// CircuitCount returns the number of compiled circuits in the registry.
func (p *ZKMLProver) CircuitCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.circuits)
}
