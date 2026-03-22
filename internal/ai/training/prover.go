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

package training

import (
	"bytes"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
)

// simulatedProofSize is the byte length of the simulated proof data.
// Matches the Groth16-like proof size used in zkml.go.
const simulatedProofSize = 256

// maxEpochCount is the upper bound on the number of training epochs allowed
// per registration. This prevents unreasonably large registrations that could
// exhaust resources during proof generation.
const maxEpochCount = 100000

// DatasetGovernance checks whether a dataset has been ethically approved.
// Implemented by governance.Committee or any external approval oracle.
type DatasetGovernance interface {
	IsApproved(datasetID types.Hash) bool
}

// TrainingProver generates and verifies zero-knowledge proofs for ML training
// correctness. It maintains a registry of training records and their
// corresponding proofs, ensuring that models are trained on approved datasets
// with verifiable computation traces.
type TrainingProver struct {
	mu         sync.Mutex
	records    map[types.Hash]*TrainingRecord
	proofs     map[types.Hash]*TrainingProof
	governance DatasetGovernance // nil = skip governance check
}

// NewTrainingProver creates a new TrainingProver with an empty registry.
// The governance parameter controls dataset approval checks; pass nil to
// skip governance validation (useful for testing).
func NewTrainingProver(gov DatasetGovernance) *TrainingProver {
	return &TrainingProver{
		records:    make(map[types.Hash]*TrainingRecord),
		proofs:     make(map[types.Hash]*TrainingProof),
		governance: gov,
	}
}

// RegisterTraining creates a new training record and registers it in the prover.
// If governance is configured, each dataset hash is checked for approval before
// registration proceeds. The record ID is deterministically computed as
// Keccak256(modelHash || sorted datasetHashes || configHash).
//
// Returns ErrDatasetNotApproved if any dataset is not approved,
// ErrNoEpochs if epochCount is not positive,
// ErrRecordExists if a record with the same ID already exists.
func (p *TrainingProver) RegisterTraining(
	modelHash types.Hash,
	datasetHashes []types.Hash,
	configHash, initWeightsHash, finalWeightsHash types.Hash,
	epochCount int,
	trainer types.Address,
) (types.Hash, error) {
	if epochCount <= 0 {
		return types.Hash{}, ErrNoEpochs
	}
	if epochCount > maxEpochCount {
		return types.Hash{}, fmt.Errorf("%w: %d exceeds %d", ErrEpochCountExceeded, epochCount, maxEpochCount)
	}
	if len(datasetHashes) == 0 {
		return types.Hash{}, ErrNoDatasets
	}

	// Deduplicate dataset hashes.
	seen := make(map[types.Hash]struct{}, len(datasetHashes))
	dedupHashes := make([]types.Hash, 0, len(datasetHashes))
	for _, dh := range datasetHashes {
		if _, dup := seen[dh]; !dup {
			seen[dh] = struct{}{}
			dedupHashes = append(dedupHashes, dh)
		}
	}

	// Compute deterministic record ID: Keccak256(modelHash || sorted datasetHashes || configHash).
	recordID := computeRecordID(modelHash, dedupHashes, configHash)

	p.mu.Lock()
	defer p.mu.Unlock()

	// Check governance approval for each dataset inside the lock to prevent
	// TOCTOU races between approval check and record insertion.
	if p.governance != nil {
		for _, dh := range dedupHashes {
			if !p.governance.IsApproved(dh) {
				return types.Hash{}, fmt.Errorf("%w: %s", ErrDatasetNotApproved, dh.Hex())
			}
		}
	}

	if _, exists := p.records[recordID]; exists {
		return types.Hash{}, fmt.Errorf("%w: %s", ErrRecordExists, recordID.Hex())
	}

	// Copy dataset hashes to prevent external mutation.
	dsCopy := make([]types.Hash, len(dedupHashes))
	copy(dsCopy, dedupHashes)

	record := &TrainingRecord{
		ID:               recordID,
		ModelHash:        modelHash,
		DatasetHashes:    dsCopy,
		ConfigHash:       configHash,
		InitWeightsHash:  initWeightsHash,
		FinalWeightsHash: finalWeightsHash,
		EpochCount:       epochCount,
		Trainer:          trainer,
		Status:           TrainingPending,
		CreatedAt:        time.Now(),
	}

	p.records[recordID] = record

	return recordID, nil
}

// ProveTraining generates a ZK proof attesting that the training described by
// the given record was computed correctly. The trace must contain epoch-level
// computation details matching the record's epoch count.
//
// The proof binds the model hash, initial/final weights, configuration, and
// dataset hashes together cryptographically. Currently uses a hash-based
// simulation for proof generation, designed for future integration with a
// real Groth16/PLONK backend.
//
// Returns ErrRecordNotFound if the record does not exist,
// ErrRecordNotPending if the record is not in Pending status,
// ErrNoEpochs if the trace has no epochs,
// ErrTraceIncomplete if the trace epoch count does not match the record.
func (p *TrainingProver) ProveTraining(recordID types.Hash, trace *TrainingTrace) (*TrainingProof, error) {
	if trace == nil {
		return nil, ErrNilTrace
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	record, exists := p.records[recordID]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrRecordNotFound, recordID.Hex())
	}
	if record.Status != TrainingPending {
		return nil, fmt.Errorf("%w: current status is %s", ErrRecordNotPending, record.Status)
	}

	// Validate trace structure (trace is an input parameter, safe to read under lock).
	if len(trace.EpochTraces) == 0 {
		return nil, ErrNoEpochs
	}
	if len(trace.EpochTraces) != record.EpochCount {
		return nil, fmt.Errorf("%w: got %d, want %d", ErrTraceIncomplete, len(trace.EpochTraces), record.EpochCount)
	}

	// Transition to Proving status.
	record.Status = TrainingProving

	// Build public inputs: modelHash(32) || initWeightsHash(32) || finalWeightsHash(32) || configHash(32) || datasetRootHash(32).
	datasetRootHash := computeDatasetRootHash(record.DatasetHashes)

	publicInputs := make([]byte, trainingPublicInputsSize)
	copy(publicInputs[0:32], record.ModelHash[:])
	copy(publicInputs[32:64], record.InitWeightsHash[:])
	copy(publicInputs[64:96], record.FinalWeightsHash[:])
	copy(publicInputs[96:128], record.ConfigHash[:])
	copy(publicInputs[128:160], datasetRootHash[:])

	// Generate proof data via hash-based simulation.
	// In production, this would invoke the SP1 prover or a native Groth16/PLONK
	// backend with the circuit and witness derived from the training trace.
	proofData := generateTrainingProof(publicInputs, trace)

	proof := &TrainingProof{
		RecordID:     recordID,
		ModelHash:    record.ModelHash,
		ProofData:    proofData,
		PublicInputs: publicInputs,
		ProofType:    TrainingProofTypeGroth16,
		GeneratedAt:  time.Now(),
	}

	// Transition to Verified status and store the proof.
	record.Status = TrainingVerified
	p.proofs[recordID] = proof

	return proof, nil
}

// generateTrainingProof creates a deterministic proof via hash-based simulation.
// The proof commits to the public inputs and epoch traces, producing a structure
// that mirrors a real ZK proof for testing and development. This will be replaced
// by a real prover backend (SP1/Groth16/PLONK).
func generateTrainingProof(publicInputs []byte, trace *TrainingTrace) []byte {
	// Build a commitment chain:
	//   commitment = H(publicInputs)
	//   for each epoch: commitment = H(commitment || weightsBeforeHash || weightsAfterHash || lossHash)
	commitment := crypto.Keccak256(publicInputs)

	// Reuse a single buffer across iterations to avoid per-epoch allocation.
	commitParts := make([]byte, 32+32+32+32)
	for i := range trace.EpochTraces {
		epoch := &trace.EpochTraces[i]
		copy(commitParts[0:32], commitment)
		copy(commitParts[32:64], epoch.WeightsBeforeHash[:])
		copy(commitParts[64:96], epoch.WeightsAfterHash[:])
		copy(commitParts[96:128], epoch.LossHash[:])
		commitment = crypto.Keccak256(commitParts)
	}

	// The "proof" is the Keccak256 hash of the commitment chain, repeated
	// to fill a realistic proof size (256 bytes for Groth16-like proofs).
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

// VerifyTrainingProof validates a training proof by checking the public inputs
// structure and proof data integrity. This performs structural validation
// suitable for development and testing. Full cryptographic verification
// requires an external verifier backend (see TrainingVerifier).
//
// Returns true if the proof is structurally valid, false otherwise.
func (p *TrainingProver) VerifyTrainingProof(proof *TrainingProof) (bool, error) {
	if proof == nil {
		return false, ErrProofNil
	}
	if len(proof.ProofData) == 0 {
		return false, ErrProofDataEmpty
	}
	if len(proof.PublicInputs) != trainingPublicInputsSize {
		return false, fmt.Errorf("%w: got %d bytes", ErrPublicInputsLength, len(proof.PublicInputs))
	}

	// Extract model hash from public inputs and verify consistency.
	var piModelHash types.Hash
	copy(piModelHash[:], proof.PublicInputs[0:32])

	if piModelHash != proof.ModelHash {
		return false, fmt.Errorf("%w: model hash", ErrPublicInputsMismatch)
	}

	// Verify proof data integrity: the first 32 bytes must be non-zero.
	var zeroHash types.Hash
	var proofPrefix types.Hash
	copy(proofPrefix[:], proof.ProofData[:32])
	if proofPrefix == zeroHash {
		return false, fmt.Errorf("%w: proof data starts with zero hash", ErrInvalidTrainingProof)
	}

	return true, nil
}

// GetRecord retrieves a training record by its ID.
// Returns the record and true if found, nil and false otherwise.
func (p *TrainingProver) GetRecord(recordID types.Hash) (*TrainingRecord, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	record, exists := p.records[recordID]
	return record, exists
}

// GetProof retrieves a training proof by the corresponding record ID.
// Returns the proof and true if found, nil and false otherwise.
func (p *TrainingProver) GetProof(recordID types.Hash) (*TrainingProof, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	proof, exists := p.proofs[recordID]
	return proof, exists
}

// RecordCount returns the number of registered training records.
func (p *TrainingProver) RecordCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.records)
}

// computeRecordID deterministically computes a training record ID as
// Keccak256(modelHash || sorted datasetHashes || configHash).
func computeRecordID(modelHash types.Hash, datasetHashes []types.Hash, configHash types.Hash) types.Hash {
	parts := sortedHashConcat(datasetHashes)
	full := make([]byte, 0, 32+len(parts)+32)
	full = append(full, modelHash[:]...)
	full = append(full, parts...)
	full = append(full, configHash[:]...)
	return crypto.Keccak256Hash(full)
}

// computeDatasetRootHash computes a root hash over the dataset hashes as
// Keccak256(sorted concatenation of datasetHashes).
func computeDatasetRootHash(datasetHashes []types.Hash) types.Hash {
	return crypto.Keccak256Hash(sortedHashConcat(datasetHashes))
}

// sortedHashConcat returns the concatenated bytes of the given hashes in
// sorted order. The input slice is not modified.
func sortedHashConcat(hashes []types.Hash) []byte {
	sorted := make([]types.Hash, len(hashes))
	copy(sorted, hashes)
	sort.Slice(sorted, func(i, j int) bool {
		return bytes.Compare(sorted[i][:], sorted[j][:]) < 0
	})
	parts := make([]byte, 0, 32*len(sorted))
	for _, h := range sorted {
		parts = append(parts, h[:]...)
	}
	return parts
}
