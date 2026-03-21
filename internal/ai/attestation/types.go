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

package attestation

import (
	"errors"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// AttestationStatus represents the lifecycle state of an inference attestation.
type AttestationStatus uint8

const (
	// AttestPending indicates the attestation has been created but not yet signed.
	AttestPending AttestationStatus = 0

	// AttestSigned indicates the attestation has been signed by the operator.
	AttestSigned AttestationStatus = 1

	// AttestVerified indicates the attestation signature has been independently verified.
	AttestVerified AttestationStatus = 2

	// AttestExpired indicates the attestation has passed its expiry time.
	AttestExpired AttestationStatus = 3

	// AttestRevoked indicates the attestation has been explicitly revoked.
	AttestRevoked AttestationStatus = 4
)

// String returns a human-readable representation of the AttestationStatus.
func (s AttestationStatus) String() string {
	switch s {
	case AttestPending:
		return "pending"
	case AttestSigned:
		return "signed"
	case AttestVerified:
		return "verified"
	case AttestExpired:
		return "expired"
	case AttestRevoked:
		return "revoked"
	default:
		return "unknown"
	}
}

// SafetyLevel classifies inference operations by risk tier, determining
// the verification stringency required for attestation.
type SafetyLevel uint8

const (
	// SafetyStandard is used for standard inference tasks (text generation, image
	// classification) where a basic ZK proof suffices.
	SafetyStandard SafetyLevel = 0

	// SafetyHighValue is used for inference that informs financial or medical
	// decisions, requiring ZK proof plus operator signature.
	SafetyHighValue SafetyLevel = 1

	// SafetyCritical is used for inference controlling autonomous systems (driving,
	// robotics), requiring ZK proof, operator signature, and verified training
	// provenance.
	SafetyCritical SafetyLevel = 2
)

// String returns a human-readable representation of the SafetyLevel.
func (l SafetyLevel) String() string {
	switch l {
	case SafetyStandard:
		return "standard"
	case SafetyHighValue:
		return "high-value"
	case SafetyCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// InferenceAttestation binds an inference result to its full provenance chain,
// linking the model, input data, output data, training record, and ZK proof
// into a single verifiable unit.
type InferenceAttestation struct {
	// ID is the unique identifier: Keccak256(requestID || modelHash || timestamp bytes).
	ID types.Hash

	// RequestID links this attestation to the original inference request.
	RequestID types.Hash

	// ModelHash identifies the model that produced the inference result.
	ModelHash types.Hash

	// InputHash is the Keccak256 digest of the inference input data.
	InputHash types.Hash

	// OutputHash is the Keccak256 digest of the inference output data.
	OutputHash types.Hash

	// TrainingRecordID links to the training record that produced the model
	// weights. Zero hash if training provenance is not applicable.
	TrainingRecordID types.Hash

	// SafetyLevel classifies the risk tier of this inference operation.
	SafetyLevel SafetyLevel

	// Status is the current lifecycle status of the attestation.
	Status AttestationStatus

	// Operator is the address of the node operator that produced the inference.
	Operator types.Address

	// OperatorDID is the W3C DID identifier for the operator (did:n42:<address>).
	OperatorDID string

	// ZKProofHash is the Keccak256 digest of the ZK proof data that validates
	// the inference computation.
	ZKProofHash types.Hash

	// CreatedAt is the time the attestation was created.
	CreatedAt time.Time

	// ExpiresAt is the time after which the attestation is considered expired.
	ExpiresAt time.Time
}

// SignedAttestation wraps an InferenceAttestation with the operator's
// cryptographic signature, binding the attestation to a specific identity.
type SignedAttestation struct {
	// Attestation is the underlying inference attestation.
	Attestation InferenceAttestation

	// Signature is the secp256k1 signature over Keccak256(canonical attestation bytes).
	Signature []byte

	// SignerPubKey is the compressed secp256k1 public key (33 bytes) of the signer.
	SignerPubKey []byte

	// SignedAt is the time the attestation was signed.
	SignedAt time.Time
}

// AttestationChain links attestations for multi-hop inference pipelines,
// establishing provenance across chained inference operations. For example,
// in autonomous driving: perception -> planning -> control, where each
// stage's output feeds the next stage's input.
type AttestationChain struct {
	// ChainID is the unique identifier: Keccak256(all attestation IDs concatenated).
	ChainID types.Hash

	// Attestations is the ordered list of attestation IDs forming the chain.
	Attestations []types.Hash

	// FinalOutput is the OutputHash of the last attestation in the chain.
	FinalOutput types.Hash

	// CreatedAt is the time the chain was created.
	CreatedAt time.Time
}

// Sentinel errors for the attestation package.
var (
	// ErrAttestationExists is returned when an attestation with the same ID already exists.
	ErrAttestationExists = errors.New("attestation: attestation already exists")

	// ErrAttestationNotFound is returned when the requested attestation does not exist.
	ErrAttestationNotFound = errors.New("attestation: attestation not found")

	// ErrAttestationExpired is returned when an attestation has passed its expiry time.
	ErrAttestationExpired = errors.New("attestation: attestation has expired")

	// ErrAttestationRevoked is returned when an attestation has been revoked.
	ErrAttestationRevoked = errors.New("attestation: attestation has been revoked")

	// ErrInvalidSignature is returned when attestation signature verification fails.
	ErrInvalidSignature = errors.New("attestation: invalid signature")

	// ErrNilPrivateKey is returned when a nil private key is provided for signing.
	ErrNilPrivateKey = errors.New("attestation: nil private key")

	// ErrModelNotVerified is returned when the model's training record is not verified.
	ErrModelNotVerified = errors.New("attestation: model training record not verified")

	// ErrZKProofRequired is returned when no valid ZK proof exists for the inference request.
	ErrZKProofRequired = errors.New("attestation: valid ZK proof required for attestation")

	// ErrChainEmpty is returned when attempting to create an attestation chain with no IDs.
	ErrChainEmpty = errors.New("attestation: chain must contain at least one attestation")

	// ErrChainBroken is returned when consecutive attestations in a chain have mismatched
	// output/input hashes, breaking the provenance link.
	ErrChainBroken = errors.New("attestation: chain link broken, output/input hash mismatch")

	// ErrTrainingRequired is returned when a safety-critical attestation requires a
	// training record but none was provided.
	ErrTrainingRequired = errors.New("attestation: training record required for safety-critical inference")

	// ErrServiceFull is returned when the attestation service has reached its maximum capacity.
	ErrServiceFull = errors.New("attestation: service has reached maximum capacity")

	// ErrZeroHash is returned when a required hash parameter is the zero hash.
	ErrZeroHash = errors.New("attestation: required hash parameter is zero")

	// ErrCompressPubkey is returned when public key compression fails.
	ErrCompressPubkey = errors.New("attestation: failed to compress public key")

	// ErrInvalidSignatureLength is returned when a signature has unexpected length.
	ErrInvalidSignatureLength = errors.New("attestation: signature must be 65 bytes")
)
