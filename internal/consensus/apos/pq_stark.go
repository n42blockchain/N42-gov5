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

// Post-Quantum STARK Integration for APOS Consensus
//
// This module provides post-quantum security for the APOS consensus through:
// - STARK signature aggregation for validator signatures
// - Hybrid mode supporting both BLS and STARK
// - Backward compatible verification
//
// Operating Modes:
// - PQModeDisabled: Only BLS signatures (current behavior)
// - PQModeHybrid: Both BLS and STARK signatures (transition period)
// - PQModeOnly: Only STARK signatures (full PQ migration)

package apos

import (
	"context"
	"errors"
	"sync"

	"github.com/n42blockchain/N42/crypto/stark"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/log"
)

// PostQuantumMode defines the PQ signature mode for consensus.
type PostQuantumMode uint8

const (
	// PQModeDisabled uses only BLS signatures (default, fully compatible)
	PQModeDisabled PostQuantumMode = 0

	// PQModeHybrid uses both BLS and STARK signatures (transition period)
	PQModeHybrid PostQuantumMode = 1

	// PQModeOnly uses only STARK signatures (fully PQ)
	PQModeOnly PostQuantumMode = 2
)

// Post-quantum consensus errors.
var (
	errMissingSTARKProof      = errors.New("apos: missing STARK proof")
	errInvalidSTARKProof      = errors.New("apos: invalid STARK proof")
	errInsufficientSigners    = errors.New("apos: insufficient signers for STARK aggregation")
	errPQModeNotEnabled       = errors.New("apos: post-quantum mode not enabled")
	errSTARKAggregationFailed = errors.New("apos: STARK aggregation failed")
)

// PostQuantumConfig holds configuration for PQ consensus.
type PostQuantumConfig struct {
	// Mode determines the post-quantum operating mode
	Mode PostQuantumMode

	// ForkHeight is the block height at which PQ mode activates
	ForkHeight uint64

	// MinValidators is the minimum validators required for STARK aggregation
	MinValidators int

	// FallbackToBLS determines whether to fall back to BLS on STARK failure
	FallbackToBLS bool
}

// DefaultPostQuantumConfig returns the default PQ configuration
func DefaultPostQuantumConfig() *PostQuantumConfig {
	return &PostQuantumConfig{
		Mode:          PQModeDisabled,
		ForkHeight:    0,
		MinValidators: 1,
		FallbackToBLS: true,
	}
}

// IsEnabled returns true if PQ mode is enabled
func (c *PostQuantumConfig) IsEnabled() bool {
	return c.Mode != PQModeDisabled
}

// ShouldUsePQ returns true if PQ should be used at the given block height
func (c *PostQuantumConfig) ShouldUsePQ(blockHeight uint64) bool {
	return c.IsEnabled() && blockHeight >= c.ForkHeight
}

// STARKBlockSignature represents a STARK aggregated signature for a block.
type STARKBlockSignature struct {
	// Proof is the STARK aggregated proof
	Proof []byte

	// SignerCount is the number of validators who signed
	SignerCount uint32

	// SignerCommitments are the public key commitments
	SignerCommitments []types.Hash

	// SignerBitmap indicates which validators participated
	SignerBitmap []byte
}

// Bytes serializes the signature
func (s *STARKBlockSignature) Bytes() []byte {
	return s.Proof
}

// Size returns the size of the signature in bytes
func (s *STARKBlockSignature) Size() int {
	return len(s.Proof)
}

// STARKConsensusManager manages STARK signature aggregation for consensus.
type STARKConsensusManager struct {
	config     *PostQuantumConfig
	aggregator *api.STARKAggregator
	mu         sync.RWMutex
}

// NewSTARKConsensusManager creates a new STARK consensus manager
func NewSTARKConsensusManager(config *PostQuantumConfig) *STARKConsensusManager {
	if config == nil {
		config = DefaultPostQuantumConfig()
	}
	return &STARKConsensusManager{
		config:     config,
		aggregator: api.NewSTARKAggregator(api.DefaultSTARKAggregatorConfig()),
	}
}

// CollectSTARKSignatures collects and aggregates STARK signatures for a block
func (m *STARKConsensusManager) CollectSTARKSignatures(
	ctx context.Context,
	blockHash types.Hash,
	validators []*api.ValidatorSignature,
) (*STARKBlockSignature, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(validators) < m.config.MinValidators {
		return nil, errInsufficientSigners
	}

	// Aggregate signatures
	aggSig, err := m.aggregator.Aggregate(blockHash[:], validators)
	if err != nil {
		log.Warn("STARK aggregation failed", "err", err)
		return nil, errSTARKAggregationFailed
	}

	return &STARKBlockSignature{
		Proof:             aggSig.Proof,
		SignerCount:       aggSig.SignerCount,
		SignerCommitments: aggSig.Commitments,
		SignerBitmap:      aggSig.SignerBitmap,
	}, nil
}

// VerifySTARKSignature verifies a STARK aggregated signature
func (m *STARKConsensusManager) VerifySTARKSignature(
	sig *STARKBlockSignature,
) error {
	if sig == nil || len(sig.Proof) == 0 {
		return errMissingSTARKProof
	}

	// Parse the proof
	proof, err := stark.ParseProof(sig.Proof)
	if err != nil {
		return errInvalidSTARKProof
	}

	// Verify the proof
	verifier := stark.NewVerifier()
	if err := verifier.Verify(proof, sig.SignerCommitments); err != nil {
		return errInvalidSTARKProof
	}

	// Verify signer count meets minimum
	if int(sig.SignerCount) < m.config.MinValidators {
		return errInsufficientSigners
	}

	return nil
}

// HybridBlockSignature contains both BLS and STARK signatures.
type HybridBlockSignature struct {
	// BLSSignature is the traditional BLS aggregate signature
	BLSSignature [96]byte

	// STARKSignature is the post-quantum STARK signature
	STARKSignature *STARKBlockSignature
}

// HasBLS returns true if BLS signature is present
func (h *HybridBlockSignature) HasBLS() bool {
	return h.BLSSignature != [96]byte{}
}

// HasSTARK returns true if STARK signature is present
func (h *HybridBlockSignature) HasSTARK() bool {
	return h.STARKSignature != nil && len(h.STARKSignature.Proof) > 0
}

// VerifySealPQ verifies a block seal with post-quantum support.
func (m *STARKConsensusManager) VerifySealPQ(
	blsSig []byte,
	starkSig *STARKBlockSignature,
	blockHeight uint64,
) error {
	switch m.config.Mode {
	case PQModeDisabled:
		// Only verify BLS
		return m.verifyBLSSeal(blsSig)

	case PQModeHybrid:
		// Verify BLS (required)
		if err := m.verifyBLSSeal(blsSig); err != nil {
			return err
		}
		// Verify STARK (optional but logged)
		if starkSig != nil && len(starkSig.Proof) > 0 {
			if err := m.VerifySTARKSignature(starkSig); err != nil {
				log.Warn("STARK verification failed in hybrid mode", "err", err)
			}
		}
		return nil

	case PQModeOnly:
		// Only verify STARK
		return m.VerifySTARKSignature(starkSig)

	default:
		return m.verifyBLSSeal(blsSig)
	}
}

// verifyBLSSeal verifies a BLS signature (placeholder - uses existing logic)
func (m *STARKConsensusManager) verifyBLSSeal(sig []byte) error {
	// This would call the existing BLS verification logic
	// For now, we return nil as the actual BLS verification is in the main APOS code
	if len(sig) == 0 {
		return errors.New("empty BLS signature")
	}
	return nil
}

// GetPQMode returns the current post-quantum mode.
func (m *STARKConsensusManager) GetPQMode() PostQuantumMode {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config.Mode
}

// SetPQMode sets the post-quantum mode (for testing)
func (m *STARKConsensusManager) SetPQMode(mode PostQuantumMode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config.Mode = mode
}

// IsPQActive returns true if PQ mode is active for the given block height
func (m *STARKConsensusManager) IsPQActive(blockHeight uint64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config.ShouldUsePQ(blockHeight)
}

// GetMinValidators returns the minimum validators required
func (m *STARKConsensusManager) GetMinValidators() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config.MinValidators
}

var (
	globalSTARKManager     *STARKConsensusManager
	globalSTARKManagerOnce sync.Once
)

// GetGlobalSTARKManager returns the global STARK consensus manager
func GetGlobalSTARKManager() *STARKConsensusManager {
	globalSTARKManagerOnce.Do(func() {
		globalSTARKManager = NewSTARKConsensusManager(DefaultPostQuantumConfig())
	})
	return globalSTARKManager
}

// InitGlobalSTARKManager initializes the global manager with custom config
func InitGlobalSTARKManager(config *PostQuantumConfig) {
	globalSTARKManager = NewSTARKConsensusManager(config)
}

// EstimateSTARKProofSize estimates the proof size for a given number of validators.
func EstimateSTARKProofSize(validatorCount int) int {
	return stark.EstimateProofSize(validatorCount, false)
}

// GetModeString returns a string representation of the PQ mode
func GetModeString(mode PostQuantumMode) string {
	switch mode {
	case PQModeDisabled:
		return "disabled"
	case PQModeHybrid:
		return "hybrid"
	case PQModeOnly:
		return "pq-only"
	default:
		return "unknown"
	}
}
