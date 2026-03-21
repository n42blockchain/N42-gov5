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
	"errors"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// TrainingStatus represents the lifecycle state of a training record.
type TrainingStatus uint8

const (
	// TrainingPending indicates the training record has been registered but not yet proven.
	TrainingPending TrainingStatus = 0

	// TrainingProving indicates proof generation is in progress.
	TrainingProving TrainingStatus = 1

	// TrainingVerified indicates the training proof has been successfully generated and verified.
	TrainingVerified TrainingStatus = 2

	// TrainingFailed indicates proof generation or verification failed.
	TrainingFailed TrainingStatus = 3
)

// String returns a human-readable representation of the training status.
func (s TrainingStatus) String() string {
	switch s {
	case TrainingPending:
		return "pending"
	case TrainingProving:
		return "proving"
	case TrainingVerified:
		return "verified"
	case TrainingFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// trainingPublicInputsSize is the expected byte length of training proof public inputs:
// modelHash (32) + initWeightsHash (32) + finalWeightsHash (32) + configHash (32) + datasetRootHash (32) = 160 bytes.
const trainingPublicInputsSize = 160

// Supported training proof types.
const (
	TrainingProofTypeGroth16 = "groth16"
	TrainingProofTypePlonk   = "plonk"
)

var (
	// ErrRecordExists is returned when a training record with the same ID already exists.
	ErrRecordExists = errors.New("training: record already exists")

	// ErrRecordNotFound is returned when the requested training record does not exist.
	ErrRecordNotFound = errors.New("training: record not found")

	// ErrTraceIncomplete is returned when the training trace has mismatched epoch count.
	ErrTraceIncomplete = errors.New("training: trace epoch count does not match record")

	// ErrNoEpochs is returned when a training record or trace has zero epochs.
	ErrNoEpochs = errors.New("training: epoch count must be positive")

	// ErrDatasetNotApproved is returned when a dataset has not been approved by governance.
	ErrDatasetNotApproved = errors.New("training: dataset not approved by governance")

	// ErrInvalidTrainingProof is returned when training proof validation fails.
	ErrInvalidTrainingProof = errors.New("training: invalid proof")

	// ErrProofNil is returned when a nil training proof is provided.
	ErrProofNil = errors.New("training: nil proof")

	// ErrProofDataEmpty is returned when training proof data is empty.
	ErrProofDataEmpty = errors.New("training: empty proof data")

	// ErrPublicInputsLength is returned when public inputs have incorrect length.
	ErrPublicInputsLength = errors.New("training: public inputs must be 160 bytes")

	// ErrPublicInputsMismatch is returned when public inputs do not match proof fields.
	ErrPublicInputsMismatch = errors.New("training: public inputs do not match proof fields")

	// ErrRecordNotPending is returned when attempting to prove a record that is not in Pending status.
	ErrRecordNotPending = errors.New("training: record is not in pending status")

	// ErrNilTrace is returned when a nil training trace is provided to ProveTraining.
	ErrNilTrace = errors.New("training: nil trace")

	// ErrNoDatasets is returned when no dataset hashes are provided to RegisterTraining.
	ErrNoDatasets = errors.New("training: no dataset hashes provided")

	// ErrEpochCountExceeded is returned when the epoch count exceeds the maximum allowed value.
	ErrEpochCountExceeded = errors.New("training: epoch count exceeds maximum")
)

// TrainingRecord represents a registered training run that can be proven.
// It captures the full provenance: which model was trained, on what data,
// with which configuration, and by whom.
type TrainingRecord struct {
	ID               types.Hash     // Keccak256(modelHash || sorted datasetHashes || configHash)
	ModelHash        types.Hash     // Identifies the model architecture being trained
	DatasetHashes    []types.Hash   // Hashes of training datasets (links to governance)
	ConfigHash       types.Hash     // Hash of training hyperparameters
	InitWeightsHash  types.Hash     // Hash of initial weights before training
	FinalWeightsHash types.Hash     // Hash of final trained weights after training
	EpochCount       int            // Number of training epochs
	Trainer          types.Address  // Address of the entity that performed training
	Status           TrainingStatus // Current lifecycle status
	CreatedAt        time.Time      // Timestamp when the record was registered
}

// TrainingTrace captures the full computation trace of a training run.
// Each epoch's intermediate state is recorded to enable ZK proof generation
// that attests to the correctness of the training process.
type TrainingTrace struct {
	RecordID    types.Hash   // Links to the corresponding TrainingRecord
	ModelHash   types.Hash   // Model being trained
	EpochTraces []EpochTrace // Per-epoch computation traces
	StartTime   time.Time    // When training started
	EndTime     time.Time    // When training completed
}

// EpochTrace captures one epoch's computation within the training pipeline.
// Each epoch records hashes of its inputs, weight transitions, loss values,
// and gradient updates, forming the witness for proof generation.
type EpochTrace struct {
	EpochIndex        int        // Zero-based epoch index
	InputDataHash     types.Hash // Hash of the batch/shard of dataset used
	WeightsBeforeHash types.Hash // Hash of weights at the start of the epoch
	WeightsAfterHash  types.Hash // Hash of weights at the end of the epoch
	LossHash          types.Hash // Hash of loss values computed during the epoch
	GradientHash      types.Hash // Hash of gradient updates applied during the epoch
}

// TrainingProof attests that a model was trained correctly from approved data.
// It binds the model identity, weight transitions, configuration, and dataset
// together cryptographically, allowing any verifier to confirm the training
// integrity without re-executing it.
type TrainingProof struct {
	RecordID     types.Hash // Links to the corresponding TrainingRecord
	ModelHash    types.Hash // Identifies the model that was trained
	ProofData    []byte     // Serialized ZK proof (Groth16 or PLONK)
	PublicInputs []byte     // 160 bytes: modelHash(32) || initWeightsHash(32) || finalWeightsHash(32) || configHash(32) || datasetRootHash(32)
	ProofType    string     // "groth16" or "plonk"
	GeneratedAt  time.Time  // Timestamp when the proof was generated
}
