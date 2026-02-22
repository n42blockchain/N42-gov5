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

// STARK Signature Aggregation API
//
// This module provides APIs for aggregating post-quantum signatures
// using STARK-style proofs for the consensus layer.

package api

import (
	"context"
	"errors"
	"sync"

	"github.com/n42blockchain/N42/common/crypto/stark"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// Errors
// =============================================================================

var (
	ErrNoSignatures          = errors.New("no signatures to aggregate")
	ErrAggregationInProgress = errors.New("aggregation already in progress")
	ErrAggregationCanceled   = errors.New("aggregation was canceled")
	ErrInvalidValidator      = errors.New("invalid validator")
	ErrSignatureTimeout      = errors.New("signature collection timeout")
)

// =============================================================================
// STARKAggregatedSignature
// =============================================================================

// STARKAggregatedSignature represents an aggregated signature proof
type STARKAggregatedSignature struct {
	// Proof is the STARK aggregated proof bytes
	Proof []byte `json:"proof"`

	// SignerCount is the number of signers included
	SignerCount uint32 `json:"signerCount"`

	// Commitments are the public key commitments of signers
	Commitments []types.Hash `json:"commitments"`

	// Message is the message that was signed
	Message types.Hash `json:"message"`

	// SignerBitmap indicates which validators signed
	SignerBitmap []byte `json:"signerBitmap,omitempty"`
}

// Bytes returns the serialized proof
func (s *STARKAggregatedSignature) Bytes() []byte {
	return s.Proof
}

// =============================================================================
// ValidatorSignature
// =============================================================================

// ValidatorSignature represents a signature from a validator
type ValidatorSignature struct {
	// ValidatorIndex is the index of the validator
	ValidatorIndex uint64 `json:"validatorIndex"`

	// PublicKey is the validator's PQ public key
	PublicKey []byte `json:"publicKey"`

	// Signature is the PQ signature
	Signature []byte `json:"signature"`

	// Algorithm is the signature algorithm used
	Algorithm uint8 `json:"algorithm"`
}

// ToSignatureInput converts to a stark.SignatureInput
func (v *ValidatorSignature) ToSignatureInput() stark.SignatureInput {
	return stark.SignatureInput{
		PublicKey: v.PublicKey,
		Signature: v.Signature,
		Algorithm: v.Algorithm,
	}
}

// =============================================================================
// SignatureCollector
// =============================================================================

// SignatureCollector collects validator signatures for aggregation
type SignatureCollector struct {
	mu         sync.RWMutex
	message    types.Hash
	signatures map[uint64]*ValidatorSignature
	threshold  int
	collected  chan struct{}
	closed     bool
}

// NewSignatureCollector creates a new signature collector
func NewSignatureCollector(message types.Hash, threshold int) *SignatureCollector {
	return &SignatureCollector{
		message:    message,
		signatures: make(map[uint64]*ValidatorSignature),
		threshold:  threshold,
		collected:  make(chan struct{}),
	}
}

// AddSignature adds a validator's signature
func (c *SignatureCollector) AddSignature(sig *ValidatorSignature) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return ErrAggregationCanceled
	}

	c.signatures[sig.ValidatorIndex] = sig

	// Check if threshold reached
	if len(c.signatures) >= c.threshold {
		select {
		case <-c.collected:
			// Already closed
		default:
			close(c.collected)
		}
	}

	return nil
}

// GetSignatures returns all collected signatures
func (c *SignatureCollector) GetSignatures() []*ValidatorSignature {
	c.mu.RLock()
	defer c.mu.RUnlock()

	sigs := make([]*ValidatorSignature, 0, len(c.signatures))
	for _, sig := range c.signatures {
		sigs = append(sigs, sig)
	}
	return sigs
}

// Count returns the number of collected signatures
func (c *SignatureCollector) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.signatures)
}

// Wait waits for threshold signatures or context cancellation
func (c *SignatureCollector) Wait(ctx context.Context) error {
	select {
	case <-c.collected:
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		return ctx.Err()
	}
}

// Close closes the collector
func (c *SignatureCollector) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

// =============================================================================
// STARKAggregator
// =============================================================================

// STARKAggregator aggregates PQ signatures into STARK proofs
type STARKAggregator struct {
	prover   *stark.Prover
	verifier *stark.Verifier
	config   STARKAggregatorConfig
}

// STARKAggregatorConfig holds configuration for the aggregator
type STARKAggregatorConfig struct {
	// MinSignatures is the minimum signatures required for aggregation
	MinSignatures int

	// MaxSignatures is the maximum signatures to include
	MaxSignatures int

	// IncludeMerkleProofs includes Merkle proofs in the output
	IncludeMerkleProofs bool
}

// DefaultSTARKAggregatorConfig returns the default configuration
func DefaultSTARKAggregatorConfig() STARKAggregatorConfig {
	return STARKAggregatorConfig{
		MinSignatures:       1,
		MaxSignatures:       1024,
		IncludeMerkleProofs: false,
	}
}

// NewSTARKAggregator creates a new STARK aggregator
func NewSTARKAggregator(config STARKAggregatorConfig) *STARKAggregator {
	proverConfig := stark.ProverConfig{
		IncludeMerkleProofs: config.IncludeMerkleProofs,
		IncludeSignerBitmap: true,
	}
	return &STARKAggregator{
		prover:   stark.NewProver(proverConfig),
		verifier: stark.NewVerifier(),
		config:   config,
	}
}

// Aggregate creates an aggregated proof from validator signatures
func (a *STARKAggregator) Aggregate(message []byte, signatures []*ValidatorSignature) (*STARKAggregatedSignature, error) {
	if len(signatures) < a.config.MinSignatures {
		return nil, ErrNoSignatures
	}

	// Convert to stark inputs
	inputs := make([]stark.SignatureInput, len(signatures))
	for i, sig := range signatures {
		inputs[i] = sig.ToSignatureInput()
	}

	// Generate aggregated proof
	proof, err := a.prover.AggregateSignatures(message, inputs)
	if err != nil {
		return nil, err
	}

	// Build commitments
	commitments := make([]types.Hash, len(signatures))
	for i, sig := range signatures {
		input := stark.SignatureInput{
			PublicKey: sig.PublicKey,
			Signature: sig.Signature,
			Algorithm: sig.Algorithm,
		}
		commitments[i] = input.PublicKeyHash()
	}

	return &STARKAggregatedSignature{
		Proof:        proof.Bytes(),
		SignerCount:  proof.SignerCount,
		Commitments:  commitments,
		Message:      proof.Message,
		SignerBitmap: proof.SignerBitmap,
	}, nil
}

// Verify verifies an aggregated proof
func (a *STARKAggregator) Verify(aggSig *STARKAggregatedSignature) error {
	proof, err := stark.ParseProof(aggSig.Proof)
	if err != nil {
		return err
	}

	return a.verifier.Verify(proof, aggSig.Commitments)
}

// VerifyWithMessage verifies a proof with the original message and signatures
func (a *STARKAggregator) VerifyWithMessage(aggSig *STARKAggregatedSignature, message []byte, signatures []*ValidatorSignature) error {
	proof, err := stark.ParseProof(aggSig.Proof)
	if err != nil {
		return err
	}

	inputs := make([]stark.SignatureInput, len(signatures))
	for i, sig := range signatures {
		inputs[i] = sig.ToSignatureInput()
	}

	return a.verifier.VerifyWithMessage(proof, message, inputs)
}

// =============================================================================
// API Methods for PublicBlockChainAPI
// =============================================================================

// STARKSignMergeRequest represents a request to aggregate signatures
type STARKSignMergeRequest struct {
	Message    types.Hash           `json:"message"`
	Signatures []ValidatorSignature `json:"signatures"`
}

// STARKSignMergeResponse represents the aggregation response
type STARKSignMergeResponse struct {
	Success       bool                      `json:"success"`
	AggregatedSig *STARKAggregatedSignature `json:"aggregatedSig,omitempty"`
	Error         string                    `json:"error,omitempty"`
}

// STARKVerifyRequest represents a verification request
type STARKVerifyRequest struct {
	Proof       []byte       `json:"proof"`
	Commitments []types.Hash `json:"commitments"`
}

// STARKVerifyResponse represents the verification response
type STARKVerifyResponse struct {
	Valid bool   `json:"valid"`
	Error string `json:"error,omitempty"`
}

// =============================================================================
// Global Aggregator Instance
// =============================================================================

var (
	globalAggregator     *STARKAggregator
	globalAggregatorOnce sync.Once
)

// GetGlobalAggregator returns the global STARK aggregator instance
func GetGlobalAggregator() *STARKAggregator {
	globalAggregatorOnce.Do(func() {
		globalAggregator = NewSTARKAggregator(DefaultSTARKAggregatorConfig())
	})
	return globalAggregator
}

// SetGlobalAggregator sets the global aggregator (for testing)
func SetGlobalAggregator(agg *STARKAggregator) {
	globalAggregator = agg
}

// =============================================================================
// Consensus Integration Helpers
// =============================================================================

// AggregateBlockSignatures aggregates signatures for a block
func AggregateBlockSignatures(blockHash types.Hash, signatures []*ValidatorSignature) (*STARKAggregatedSignature, error) {
	aggregator := GetGlobalAggregator()
	return aggregator.Aggregate(blockHash[:], signatures)
}

// VerifyBlockSignatures verifies aggregated signatures for a block
func VerifyBlockSignatures(aggSig *STARKAggregatedSignature) error {
	aggregator := GetGlobalAggregator()
	return aggregator.Verify(aggSig)
}

// CreateSignerBitmap creates a bitmap for participating signers
func CreateSignerBitmap(validatorCount int, signers []uint64) []byte {
	byteCount := (validatorCount + 7) / 8
	bitmap := make([]byte, byteCount)
	for _, idx := range signers {
		if int(idx) < validatorCount {
			bitmap[idx/8] |= 1 << (idx % 8)
		}
	}
	return bitmap
}

// ParseSignerBitmap returns the indices of signers from a bitmap
func ParseSignerBitmap(bitmap []byte) []uint64 {
	var signers []uint64
	for byteIdx, b := range bitmap {
		for bitIdx := 0; bitIdx < 8; bitIdx++ {
			if b&(1<<bitIdx) != 0 {
				signers = append(signers, uint64(byteIdx*8+bitIdx))
			}
		}
	}
	return signers
}

// CountSigners counts the number of signers in a bitmap
func CountSigners(bitmap []byte) int {
	return stark.CountSignersInBitmap(bitmap)
}
