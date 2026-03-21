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
	"bytes"
	"crypto/ecdsa"
	"encoding/binary"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
)

// ZKProofProvider abstracts ZK proof availability for inference requests.
// Implementations bridge the attestation service to the underlying ZK proving
// infrastructure (e.g., STARK/SNARK/SP1 backends).
type ZKProofProvider interface {
	// GetProof retrieves the ZK proof data for the given inference request.
	// Returns the raw proof bytes, whether the proof is valid, and any error.
	GetProof(requestID types.Hash) (proofData []byte, valid bool, err error)
}

// TrainingVerification abstracts training record verification, bridging to
// the training provenance subsystem (Feature 2). Implementations check whether
// a training record exists and has been verified.
type TrainingVerification interface {
	// GetRecord retrieves the model hash and verification status for a training
	// record. Returns the model hash associated with the record, whether the
	// record has been verified, and any error.
	GetRecord(recordID types.Hash) (modelHash types.Hash, verified bool, err error)
}

// AttestationService manages the lifecycle of inference attestations, providing
// creation, signing, verification, chaining, and pruning. All methods are safe
// for concurrent use.
type AttestationService struct {
	mu            sync.RWMutex
	attestations  map[types.Hash]*SignedAttestation
	chains        map[types.Hash]*AttestationChain
	byRequest     map[types.Hash]types.Hash // requestID -> attestation ID
	zkProvider    ZKProofProvider
	trainingCheck TrainingVerification // optional; nil if Feature 2 not enabled
	ttl           time.Duration
	maxItems      int
}

// NewAttestationService creates a new AttestationService with the given
// dependencies. The trainingCheck parameter may be nil if training provenance
// verification is not enabled. If maxItems <= 0, no capacity limit is enforced.
func NewAttestationService(
	zkProvider ZKProofProvider,
	trainingCheck TrainingVerification,
	ttl time.Duration,
	maxItems int,
) *AttestationService {
	return &AttestationService{
		attestations:  make(map[types.Hash]*SignedAttestation),
		chains:        make(map[types.Hash]*AttestationChain),
		byRequest:     make(map[types.Hash]types.Hash),
		zkProvider:    zkProvider,
		trainingCheck: trainingCheck,
		ttl:           ttl,
		maxItems:      maxItems,
	}
}

// CreateAttestation creates a new inference attestation, binding an inference
// result to its provenance chain. The ZK proof for the given requestID must
// exist and be valid. For safety-critical inference, a verified training record
// is also required.
//
// Errors:
//   - ErrZKProofRequired if no valid ZK proof exists for the request
//   - ErrTrainingRequired if safetyLevel is Critical and trainingRecordID is zero
//   - ErrModelNotVerified if training record exists but is not verified
//   - ErrServiceFull if the service has reached maxItems capacity
//   - ErrAttestationExists if an attestation with the derived ID already exists
func (s *AttestationService) CreateAttestation(
	requestID, modelHash, inputHash, outputHash types.Hash,
	trainingRecordID types.Hash,
	safetyLevel SafetyLevel,
	operator types.Address,
	operatorDID string,
) (*InferenceAttestation, error) {
	// Validate required hash parameters are non-zero.
	zeroHash := types.Hash{}
	if requestID == zeroHash || modelHash == zeroHash || inputHash == zeroHash || outputHash == zeroHash {
		return nil, ErrZeroHash
	}

	// Verify ZK proof exists and is valid.
	proofData, valid, err := s.zkProvider.GetProof(requestID)
	if err != nil {
		return nil, err
	}
	if !valid || len(proofData) == 0 {
		return nil, ErrZKProofRequired
	}

	// For safety-critical inference, verify training provenance.
	if safetyLevel == SafetyCritical {
		if trainingRecordID == zeroHash {
			return nil, ErrTrainingRequired
		}
		if s.trainingCheck != nil {
			recordModel, verified, tErr := s.trainingCheck.GetRecord(trainingRecordID)
			if tErr != nil {
				return nil, tErr
			}
			if !verified {
				return nil, ErrModelNotVerified
			}
			// Ensure the training record's model matches the attestation model.
			if recordModel != modelHash {
				return nil, ErrModelNotVerified
			}
		}
	}

	now := time.Now()

	// Derive deterministic attestation ID: Keccak256(requestID || modelHash || timestamp bytes).
	tsBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(tsBuf, uint64(now.UnixNano()))
	idData := make([]byte, 0, types.HashLength+types.HashLength+8)
	idData = append(idData, requestID.Bytes()...)
	idData = append(idData, modelHash.Bytes()...)
	idData = append(idData, tsBuf...)
	attestID := types.BytesToHash(crypto.Keccak256(idData))

	// Compute ZK proof hash.
	zkProofHash := types.BytesToHash(crypto.Keccak256(proofData))

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.maxItems > 0 && len(s.attestations) >= s.maxItems {
		return nil, ErrServiceFull
	}
	if _, exists := s.attestations[attestID]; exists {
		return nil, ErrAttestationExists
	}

	att := &InferenceAttestation{
		ID:               attestID,
		RequestID:        requestID,
		ModelHash:        modelHash,
		InputHash:        inputHash,
		OutputHash:       outputHash,
		TrainingRecordID: trainingRecordID,
		SafetyLevel:      safetyLevel,
		Status:           AttestPending,
		Operator:         operator,
		OperatorDID:      operatorDID,
		ZKProofHash:      zkProofHash,
		CreatedAt:        now,
		ExpiresAt:        now.Add(s.ttl),
	}

	s.attestations[attestID] = &SignedAttestation{Attestation: *att}
	s.byRequest[requestID] = attestID

	// Return a copy to prevent caller mutation.
	result := *att
	return &result, nil
}

// canonicalBytes produces the canonical byte representation of an attestation
// for signing and verification:
// attestID(32) || modelHash(32) || inputHash(32) || outputHash(32) || safetyLevel(1) || timestamp(8 big-endian)
func canonicalBytes(att *InferenceAttestation) []byte {
	buf := make([]byte, 0, types.HashLength*4+1+8)
	buf = append(buf, att.ID.Bytes()...)
	buf = append(buf, att.ModelHash.Bytes()...)
	buf = append(buf, att.InputHash.Bytes()...)
	buf = append(buf, att.OutputHash.Bytes()...)
	buf = append(buf, byte(att.SafetyLevel))
	tsBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(tsBuf, uint64(att.CreatedAt.UnixNano()))
	buf = append(buf, tsBuf...)
	return buf
}

// SignAttestation signs an attestation with the operator's private key. The
// attestation must be in AttestPending status. On success, the attestation
// transitions to AttestSigned and the signed wrapper is returned.
//
// Errors:
//   - ErrNilPrivateKey if privateKey is nil
//   - ErrAttestationNotFound if the attestation does not exist
//   - ErrAttestationRevoked if the attestation has been revoked
//   - ErrAttestationExpired if the attestation has expired
//   - ErrInvalidSignature if the attestation is not in Pending status
func (s *AttestationService) SignAttestation(attestID types.Hash, privateKey *ecdsa.PrivateKey) (*SignedAttestation, error) {
	if privateKey == nil {
		return nil, ErrNilPrivateKey
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	signed, ok := s.attestations[attestID]
	if !ok {
		return nil, ErrAttestationNotFound
	}

	att := &signed.Attestation
	if att.Status != AttestPending {
		switch att.Status {
		case AttestRevoked:
			return nil, ErrAttestationRevoked
		case AttestExpired:
			return nil, ErrAttestationExpired
		default:
			return nil, ErrInvalidSignature
		}
	}

	// Check expiry.
	if time.Now().After(att.ExpiresAt) {
		att.Status = AttestExpired
		return nil, ErrAttestationExpired
	}

	// Build canonical representation and hash.
	canonical := canonicalBytes(att)
	hash := crypto.Keccak256(canonical)

	// Sign the hash.
	sig, err := crypto.Sign(hash, privateKey)
	if err != nil {
		return nil, err
	}
	if len(sig) != 65 {
		return nil, ErrInvalidSignatureLength
	}

	// Extract compressed public key.
	pubKeyBytes := crypto.CompressPubkey(&privateKey.PublicKey)
	if pubKeyBytes == nil {
		return nil, ErrCompressPubkey
	}

	now := time.Now()
	att.Status = AttestSigned
	signed.Signature = sig
	signed.SignerPubKey = pubKeyBytes
	signed.SignedAt = now

	// Return a copy.
	result := copySignedAttestation(signed)
	return result, nil
}

// VerifySignature verifies the cryptographic signature on a SignedAttestation.
// It rebuilds the canonical bytes, recovers the public key from the signature,
// and compares it against the stored signer public key.
//
// Returns true if the signature is valid, false otherwise.
func (s *AttestationService) VerifySignature(signed *SignedAttestation) (bool, error) {
	if signed == nil {
		return false, ErrAttestationNotFound
	}
	if len(signed.Signature) == 0 || len(signed.SignerPubKey) == 0 {
		return false, ErrInvalidSignature
	}

	// Rebuild canonical bytes and hash.
	canonical := canonicalBytes(&signed.Attestation)
	hash := crypto.Keccak256(canonical)

	// Recover the public key from the signature.
	recoveredPub, err := crypto.SigToPub(hash, signed.Signature)
	if err != nil {
		return false, err
	}

	// Compress the recovered key and compare.
	recoveredCompressed := crypto.CompressPubkey(recoveredPub)
	if !bytes.Equal(recoveredCompressed, signed.SignerPubKey) {
		return false, ErrInvalidSignature
	}

	return true, nil
}

// VerifyAttestation performs full verification of an attestation: checks that
// the signature is valid, the attestation has not expired, and has not been
// revoked. On successful verification, the status transitions to AttestVerified.
//
// Errors:
//   - ErrAttestationNotFound if the attestation does not exist
//   - ErrAttestationRevoked if the attestation has been revoked
//   - ErrAttestationExpired if the attestation has expired
//   - ErrInvalidSignature if signature verification fails
func (s *AttestationService) VerifyAttestation(attestID types.Hash) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	signed, ok := s.attestations[attestID]
	if !ok {
		return false, ErrAttestationNotFound
	}

	att := &signed.Attestation

	// Check revocation.
	if att.Status == AttestRevoked {
		return false, ErrAttestationRevoked
	}

	// Check expiry.
	if time.Now().After(att.ExpiresAt) {
		att.Status = AttestExpired
		return false, ErrAttestationExpired
	}

	// Verify signature.
	valid, err := s.VerifySignature(signed)
	if err != nil {
		return false, err
	}
	if !valid {
		return false, ErrInvalidSignature
	}

	att.Status = AttestVerified
	return true, nil
}

// GetAttestation retrieves a signed attestation by its ID.
// Returns a copy of the attestation and true if found, nil and false otherwise.
func (s *AttestationService) GetAttestation(attestID types.Hash) (*SignedAttestation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	signed, ok := s.attestations[attestID]
	if !ok {
		return nil, false
	}
	return copySignedAttestation(signed), true
}

// GetByRequest retrieves a signed attestation by the original inference request ID.
// Returns a copy of the attestation and true if found, nil and false otherwise.
func (s *AttestationService) GetByRequest(requestID types.Hash) (*SignedAttestation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	attestID, ok := s.byRequest[requestID]
	if !ok {
		return nil, false
	}
	signed, ok := s.attestations[attestID]
	if !ok {
		return nil, false
	}
	return copySignedAttestation(signed), true
}

// RevokeAttestation sets the status of an attestation to AttestRevoked.
//
// Errors:
//   - ErrAttestationNotFound if the attestation does not exist
func (s *AttestationService) RevokeAttestation(attestID types.Hash) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	signed, ok := s.attestations[attestID]
	if !ok {
		return ErrAttestationNotFound
	}
	signed.Attestation.Status = AttestRevoked
	return nil
}

// CreateChain creates an attestation chain from the given ordered attestation IDs.
// Each consecutive pair must have matching output/input hashes to establish
// provenance continuity. All attestations must exist in the service.
//
// Errors:
//   - ErrChainEmpty if attestationIDs is empty
//   - ErrAttestationNotFound if any attestation ID does not exist
//   - ErrChainBroken if consecutive attestations have mismatched output/input hashes
func (s *AttestationService) CreateChain(attestationIDs []types.Hash) (*AttestationChain, error) {
	if len(attestationIDs) == 0 {
		return nil, ErrChainEmpty
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate all attestations exist and chain links are valid.
	for i, id := range attestationIDs {
		signed, ok := s.attestations[id]
		if !ok {
			return nil, ErrAttestationNotFound
		}

		// Check chain continuity: output[i] must equal input[i+1].
		if i > 0 {
			prevSigned := s.attestations[attestationIDs[i-1]]
			if prevSigned.Attestation.OutputHash != signed.Attestation.InputHash {
				return nil, ErrChainBroken
			}
		}
	}

	// Derive chain ID: Keccak256(all attestation IDs concatenated).
	idData := make([]byte, 0, len(attestationIDs)*types.HashLength)
	for _, id := range attestationIDs {
		idData = append(idData, id.Bytes()...)
	}
	chainID := types.BytesToHash(crypto.Keccak256(idData))

	// Get the final output from the last attestation.
	lastSigned := s.attestations[attestationIDs[len(attestationIDs)-1]]
	finalOutput := lastSigned.Attestation.OutputHash

	// Copy the attestation IDs to prevent caller mutation.
	ids := make([]types.Hash, len(attestationIDs))
	copy(ids, attestationIDs)

	chain := &AttestationChain{
		ChainID:      chainID,
		Attestations: ids,
		FinalOutput:  finalOutput,
		CreatedAt:    time.Now(),
	}

	s.chains[chainID] = chain

	// Return a copy.
	result := copyChain(chain)
	return result, nil
}

// VerifyChain verifies an attestation chain by checking that each attestation
// in the chain is individually valid and that the chain links are intact
// (each output feeds the next input).
//
// Errors:
//   - ErrAttestationNotFound if the chain or any attestation does not exist
//   - ErrChainBroken if consecutive attestations have mismatched output/input hashes
//   - Other errors from individual attestation verification
func (s *AttestationService) VerifyChain(chainID types.Hash) (bool, error) {
	// Snapshot the chain data and all attestation copies under a single read lock.
	s.mu.RLock()
	chain, ok := s.chains[chainID]
	if !ok {
		s.mu.RUnlock()
		return false, ErrAttestationNotFound
	}
	ids := make([]types.Hash, len(chain.Attestations))
	copy(ids, chain.Attestations)

	// Deep-copy all attestations in the chain so we can verify without locks.
	snapshots := make([]*SignedAttestation, len(ids))
	for i, id := range ids {
		signed, exists := s.attestations[id]
		if !exists {
			s.mu.RUnlock()
			return false, ErrAttestationNotFound
		}
		snapshots[i] = copySignedAttestation(signed)
	}
	s.mu.RUnlock()

	// Verify each attestation's signature independently using snapshot copies.
	// This avoids calling VerifyAttestation (which acquires a write lock).
	for _, snap := range snapshots {
		att := &snap.Attestation

		// Check revocation.
		if att.Status == AttestRevoked {
			return false, ErrAttestationRevoked
		}

		// Check expiry.
		if time.Now().After(att.ExpiresAt) {
			return false, ErrAttestationExpired
		}

		// Verify signature on the snapshot copy.
		valid, err := s.VerifySignature(snap)
		if err != nil {
			return false, err
		}
		if !valid {
			return false, ErrInvalidSignature
		}
	}

	// Validate chain links using the snapshot data (no lock needed, data is local).
	for i := 1; i < len(snapshots); i++ {
		if snapshots[i-1].Attestation.OutputHash != snapshots[i].Attestation.InputHash {
			return false, ErrChainBroken
		}
	}

	// Mark attestations as verified under write lock.
	s.mu.Lock()
	for _, id := range ids {
		if signed, exists := s.attestations[id]; exists {
			signed.Attestation.Status = AttestVerified
		}
	}
	s.mu.Unlock()

	return true, nil
}

// Prune removes expired attestations from the service and returns the number
// of attestations removed. Expired attestations are also removed from the
// request index.
func (s *AttestationService) Prune() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	pruned := 0

	for id, signed := range s.attestations {
		if now.After(signed.Attestation.ExpiresAt) {
			// Remove from request index only if it still points to this attestation.
			reqID := signed.Attestation.RequestID
			if mappedID, ok := s.byRequest[reqID]; ok && mappedID == id {
				delete(s.byRequest, reqID)
			}
			// Remove the attestation.
			delete(s.attestations, id)
			pruned++
		}
	}

	return pruned
}

// Count returns the number of attestations currently in the service.
func (s *AttestationService) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.attestations)
}

// copySignedAttestation returns a deep copy of a SignedAttestation to prevent
// callers from mutating internal service state.
func copySignedAttestation(sa *SignedAttestation) *SignedAttestation {
	cp := *sa
	cp.Attestation = sa.Attestation

	if len(sa.Signature) > 0 {
		cp.Signature = make([]byte, len(sa.Signature))
		copy(cp.Signature, sa.Signature)
	}
	if len(sa.SignerPubKey) > 0 {
		cp.SignerPubKey = make([]byte, len(sa.SignerPubKey))
		copy(cp.SignerPubKey, sa.SignerPubKey)
	}

	return &cp
}

// copyChain returns a deep copy of an AttestationChain to prevent callers from
// mutating internal service state.
func copyChain(c *AttestationChain) *AttestationChain {
	cp := *c
	if len(c.Attestations) > 0 {
		cp.Attestations = make([]types.Hash, len(c.Attestations))
		copy(cp.Attestations, c.Attestations)
	}
	return &cp
}
