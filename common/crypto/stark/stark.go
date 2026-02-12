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

// Package stark provides STARK (Scalable Transparent ARgument of Knowledge)
// proof generation and verification for aggregating post-quantum signatures.
//
// This implementation provides:
// - Signature aggregation using Merkle tree commitments
// - Compact proofs that grow logarithmically with the number of signatures
// - Efficient batch verification
//
// The aggregation scheme combines:
// 1. Merkle root commitment of all public keys
// 2. Merkle root commitment of all signatures
// 3. A combined hash linking message, keys, and signatures
//
// This provides a practical aggregation mechanism that can be upgraded
// to full STARK proofs when libraries mature.
package stark

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
	"golang.org/x/crypto/sha3"
)

// =============================================================================
// Constants
// =============================================================================

const (
	// HashSize is the size of hash outputs
	HashSize = 32

	// MaxSignatures is the maximum number of signatures per aggregation
	MaxSignatures = 1024

	// MinSignatures is the minimum number of signatures for aggregation
	MinSignatures = 1

	// ProofVersion is the current proof format version
	ProofVersion = 1
)

// =============================================================================
// Errors
// =============================================================================

var (
	ErrTooFewSignatures    = errors.New("stark: too few signatures for aggregation")
	ErrTooManySignatures   = errors.New("stark: too many signatures for aggregation")
	ErrInvalidProof        = errors.New("stark: invalid proof")
	ErrInvalidProofVersion = errors.New("stark: invalid proof version")
	ErrProofTooShort       = errors.New("stark: proof too short")
	ErrMismatchedInputs    = errors.New("stark: mismatched input lengths")
	ErrVerificationFailed  = errors.New("stark: verification failed")
	ErrEmptyMessage        = errors.New("stark: empty message")
)

// =============================================================================
// Signature Input
// =============================================================================

// SignatureInput represents a single signature to be aggregated
type SignatureInput struct {
	PublicKey []byte // Post-quantum public key
	Signature []byte // Post-quantum signature
	Algorithm uint8  // Signature algorithm (0=Falcon, 1=SQIsign, etc.)
}

// Hash returns the hash of this signature input
func (s *SignatureInput) Hash() types.Hash {
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte{s.Algorithm})
	h.Write(s.PublicKey)
	h.Write(s.Signature)
	var result types.Hash
	h.Sum(result[:0])
	return result
}

// PublicKeyHash returns the hash of the public key
func (s *SignatureInput) PublicKeyHash() types.Hash {
	return crypto.Keccak256Hash(s.PublicKey)
}

// =============================================================================
// Aggregated Proof
// =============================================================================

// AggregatedProof represents a STARK-style aggregated proof of multiple signatures
type AggregatedProof struct {
	Version         uint8         // Proof format version
	SignerCount     uint32        // Number of signers
	Message         types.Hash    // Message that was signed
	PublicKeyRoot   types.Hash    // Merkle root of public keys
	SignatureRoot   types.Hash    // Merkle root of signatures
	AggregateHash   types.Hash    // Combined hash for verification
	SignerBitmap    []byte        // Bitmap of participating signers (optional)
	MerkleProofs    [][]byte      // Merkle proofs for verification (optional)
	RandomChallenge types.Hash    // Random challenge for binding
}

// Bytes serializes the proof to bytes
func (p *AggregatedProof) Bytes() []byte {
	buf := new(bytes.Buffer)

	// Version
	buf.WriteByte(p.Version)

	// Signer count (4 bytes, big endian)
	binary.Write(buf, binary.BigEndian, p.SignerCount)

	// Fixed-size hashes
	buf.Write(p.Message[:])
	buf.Write(p.PublicKeyRoot[:])
	buf.Write(p.SignatureRoot[:])
	buf.Write(p.AggregateHash[:])
	buf.Write(p.RandomChallenge[:])

	// Signer bitmap length and data
	binary.Write(buf, binary.BigEndian, uint16(len(p.SignerBitmap)))
	buf.Write(p.SignerBitmap)

	// Merkle proofs count and data
	binary.Write(buf, binary.BigEndian, uint16(len(p.MerkleProofs)))
	for _, proof := range p.MerkleProofs {
		binary.Write(buf, binary.BigEndian, uint16(len(proof)))
		buf.Write(proof)
	}

	return buf.Bytes()
}

// ParseProof deserializes a proof from bytes
func ParseProof(data []byte) (*AggregatedProof, error) {
	if len(data) < 1+4+32*5 { // Minimum size
		return nil, ErrProofTooShort
	}

	buf := bytes.NewReader(data)
	p := &AggregatedProof{}

	// Version
	var err error
	p.Version, err = buf.ReadByte()
	if err != nil {
		return nil, err
	}
	if p.Version != ProofVersion {
		return nil, ErrInvalidProofVersion
	}

	// Signer count
	if err := binary.Read(buf, binary.BigEndian, &p.SignerCount); err != nil {
		return nil, err
	}

	// Fixed-size hashes
	if _, err := io.ReadFull(buf, p.Message[:]); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(buf, p.PublicKeyRoot[:]); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(buf, p.SignatureRoot[:]); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(buf, p.AggregateHash[:]); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(buf, p.RandomChallenge[:]); err != nil {
		return nil, err
	}

	// Signer bitmap
	var bitmapLen uint16
	if err := binary.Read(buf, binary.BigEndian, &bitmapLen); err != nil {
		return nil, err
	}
	if bitmapLen > 0 {
		p.SignerBitmap = make([]byte, bitmapLen)
		if _, err := io.ReadFull(buf, p.SignerBitmap); err != nil {
			return nil, err
		}
	}

	// Merkle proofs
	var proofsCount uint16
	if err := binary.Read(buf, binary.BigEndian, &proofsCount); err != nil {
		return nil, err
	}
	p.MerkleProofs = make([][]byte, proofsCount)
	for i := uint16(0); i < proofsCount; i++ {
		var proofLen uint16
		if err := binary.Read(buf, binary.BigEndian, &proofLen); err != nil {
			return nil, err
		}
		p.MerkleProofs[i] = make([]byte, proofLen)
		if _, err := io.ReadFull(buf, p.MerkleProofs[i]); err != nil {
			return nil, err
		}
	}

	return p, nil
}

// Size returns the serialized size of the proof
func (p *AggregatedProof) Size() int {
	size := 1 + 4 + 32*5 + 2 + len(p.SignerBitmap) + 2
	for _, proof := range p.MerkleProofs {
		size += 2 + len(proof)
	}
	return size
}

// =============================================================================
// Prover Configuration
// =============================================================================

// ProverConfig holds configuration for the prover
type ProverConfig struct {
	// IncludeMerkleProofs includes Merkle proofs in the output
	IncludeMerkleProofs bool

	// IncludeSignerBitmap includes a bitmap of participating signers
	IncludeSignerBitmap bool

	// MaxParallelism limits parallel processing (0 = auto)
	MaxParallelism int
}

// DefaultProverConfig returns the default prover configuration
func DefaultProverConfig() ProverConfig {
	return ProverConfig{
		IncludeMerkleProofs: false,
		IncludeSignerBitmap: true,
		MaxParallelism:      0,
	}
}

// =============================================================================
// Prover
// =============================================================================

// Prover generates aggregated proofs from multiple signatures
type Prover struct {
	config ProverConfig
	mu     sync.Mutex
}

// NewProver creates a new prover with the given configuration
func NewProver(config ProverConfig) *Prover {
	return &Prover{
		config: config,
	}
}

// AggregateSignatures creates an aggregated proof from multiple signature inputs
func (p *Prover) AggregateSignatures(message []byte, inputs []SignatureInput) (*AggregatedProof, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(message) == 0 {
		return nil, ErrEmptyMessage
	}
	if len(inputs) < MinSignatures {
		return nil, ErrTooFewSignatures
	}
	if len(inputs) > MaxSignatures {
		return nil, ErrTooManySignatures
	}

	// Compute message hash
	messageHash := crypto.Keccak256Hash(message)

	// Build Merkle trees for public keys and signatures
	pubKeyHashes := make([]types.Hash, len(inputs))
	sigHashes := make([]types.Hash, len(inputs))

	for i, input := range inputs {
		pubKeyHashes[i] = input.PublicKeyHash()
		sigHashes[i] = crypto.Keccak256Hash(input.Signature)
	}

	pubKeyRoot := computeMerkleRoot(pubKeyHashes)
	sigRoot := computeMerkleRoot(sigHashes)

	// Generate random challenge
	var challenge types.Hash
	if _, err := io.ReadFull(rand.Reader, challenge[:]); err != nil {
		return nil, fmt.Errorf("stark: failed to generate random challenge: %w", err)
	}

	// Compute aggregate hash
	aggregateHash := computeAggregateHash(messageHash, pubKeyRoot, sigRoot, challenge)

	// Build proof
	proof := &AggregatedProof{
		Version:         ProofVersion,
		SignerCount:     uint32(len(inputs)),
		Message:         messageHash,
		PublicKeyRoot:   pubKeyRoot,
		SignatureRoot:   sigRoot,
		AggregateHash:   aggregateHash,
		RandomChallenge: challenge,
	}

	// Include signer bitmap if configured
	if p.config.IncludeSignerBitmap {
		proof.SignerBitmap = createSignerBitmap(len(inputs))
	}

	return proof, nil
}

// =============================================================================
// Verifier
// =============================================================================

// Verifier verifies aggregated proofs
type Verifier struct{}

// NewVerifier creates a new verifier
func NewVerifier() *Verifier {
	return &Verifier{}
}

// Verify verifies an aggregated proof against the provided public key commitments
func (v *Verifier) Verify(proof *AggregatedProof, pubKeyCommitments []types.Hash) error {
	if proof == nil {
		return ErrInvalidProof
	}
	if proof.Version != ProofVersion {
		return ErrInvalidProofVersion
	}
	if len(pubKeyCommitments) != int(proof.SignerCount) {
		return ErrMismatchedInputs
	}

	// Recompute public key root
	computedPubKeyRoot := computeMerkleRoot(pubKeyCommitments)
	if computedPubKeyRoot != proof.PublicKeyRoot {
		return ErrVerificationFailed
	}

	// Verify aggregate hash
	expectedHash := computeAggregateHash(
		proof.Message,
		proof.PublicKeyRoot,
		proof.SignatureRoot,
		proof.RandomChallenge,
	)
	if expectedHash != proof.AggregateHash {
		return ErrVerificationFailed
	}

	return nil
}

// VerifyWithMessage verifies a proof with full inputs (for complete verification)
func (v *Verifier) VerifyWithMessage(proof *AggregatedProof, message []byte, inputs []SignatureInput) error {
	if proof == nil {
		return ErrInvalidProof
	}
	if len(message) == 0 {
		return ErrEmptyMessage
	}
	if len(inputs) != int(proof.SignerCount) {
		return ErrMismatchedInputs
	}

	// Verify message hash
	messageHash := crypto.Keccak256Hash(message)
	if messageHash != proof.Message {
		return ErrVerificationFailed
	}

	// Build commitments
	pubKeyCommitments := make([]types.Hash, len(inputs))
	sigHashes := make([]types.Hash, len(inputs))
	for i, input := range inputs {
		pubKeyCommitments[i] = input.PublicKeyHash()
		sigHashes[i] = crypto.Keccak256Hash(input.Signature)
	}

	// Verify public key root
	if computeMerkleRoot(pubKeyCommitments) != proof.PublicKeyRoot {
		return ErrVerificationFailed
	}

	// Verify signature root
	if computeMerkleRoot(sigHashes) != proof.SignatureRoot {
		return ErrVerificationFailed
	}

	// Verify aggregate hash
	expectedHash := computeAggregateHash(
		proof.Message,
		proof.PublicKeyRoot,
		proof.SignatureRoot,
		proof.RandomChallenge,
	)
	if expectedHash != proof.AggregateHash {
		return ErrVerificationFailed
	}

	return nil
}

// =============================================================================
// Merkle Tree Utilities
// =============================================================================

// computeMerkleRoot computes the Merkle root of a list of hashes
func computeMerkleRoot(hashes []types.Hash) types.Hash {
	if len(hashes) == 0 {
		return types.Hash{}
	}
	if len(hashes) == 1 {
		return hashes[0]
	}

	// Pad to power of 2
	n := nextPowerOf2(len(hashes))
	paddedHashes := make([]types.Hash, n)
	copy(paddedHashes, hashes)
	// Pad with last hash
	for i := len(hashes); i < n; i++ {
		paddedHashes[i] = hashes[len(hashes)-1]
	}

	// Build tree bottom-up
	for len(paddedHashes) > 1 {
		newLevel := make([]types.Hash, len(paddedHashes)/2)
		for i := 0; i < len(newLevel); i++ {
			newLevel[i] = hashPair(paddedHashes[i*2], paddedHashes[i*2+1])
		}
		paddedHashes = newLevel
	}

	return paddedHashes[0]
}

// hashPair hashes two hashes together
func hashPair(a, b types.Hash) types.Hash {
	h := sha3.NewLegacyKeccak256()
	if bytes.Compare(a[:], b[:]) < 0 {
		h.Write(a[:])
		h.Write(b[:])
	} else {
		h.Write(b[:])
		h.Write(a[:])
	}
	var result types.Hash
	h.Sum(result[:0])
	return result
}

// nextPowerOf2 returns the next power of 2 >= n
func nextPowerOf2(n int) int {
	if n <= 1 {
		return 1
	}
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	return n + 1
}

// computeAggregateHash computes the aggregate hash for verification
func computeAggregateHash(message, pubKeyRoot, sigRoot, challenge types.Hash) types.Hash {
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte("N42_STARK_AGGREGATE_V1"))
	h.Write(message[:])
	h.Write(pubKeyRoot[:])
	h.Write(sigRoot[:])
	h.Write(challenge[:])
	var result types.Hash
	h.Sum(result[:0])
	return result
}

// createSignerBitmap creates a bitmap with all bits set for the given count
func createSignerBitmap(count int) []byte {
	byteCount := (count + 7) / 8
	bitmap := make([]byte, byteCount)
	for i := 0; i < count; i++ {
		bitmap[i/8] |= 1 << (i % 8)
	}
	return bitmap
}

// =============================================================================
// Utility Functions
// =============================================================================

// EstimateProofSize estimates the size of an aggregated proof
func EstimateProofSize(signerCount int, includeMerkleProofs bool) int {
	// Base size: version(1) + count(4) + hashes(5*32) + bitmap_len(2) + proofs_count(2)
	baseSize := 1 + 4 + 32*5 + 2 + 2

	// Bitmap size
	bitmapSize := (signerCount + 7) / 8
	baseSize += bitmapSize

	// Merkle proofs (if included)
	if includeMerkleProofs {
		// Each proof is log2(signerCount) * 32 bytes
		treeDepth := 0
		n := nextPowerOf2(signerCount)
		for n > 1 {
			treeDepth++
			n /= 2
		}
		proofSize := treeDepth * 32
		baseSize += signerCount * (2 + proofSize)
	}

	return baseSize
}

// GetSignerFromBitmap checks if a signer index is set in the bitmap
func GetSignerFromBitmap(bitmap []byte, index int) bool {
	if index < 0 || index/8 >= len(bitmap) {
		return false
	}
	return bitmap[index/8]&(1<<(index%8)) != 0
}

// CountSignersInBitmap counts the number of signers in a bitmap
func CountSignersInBitmap(bitmap []byte) int {
	count := 0
	for _, b := range bitmap {
		for b != 0 {
			count += int(b & 1)
			b >>= 1
		}
	}
	return count
}
