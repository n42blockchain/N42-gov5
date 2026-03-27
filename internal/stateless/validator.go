// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package stateless provides stateless block validation using witnesses.
// A stateless validator verifies blocks using only the block data and an
// accompanying witness (JMT Merkle proofs), without requiring access to
// the full state database.
//
// Two validation levels:
//   - Proof-only: verifies Merkle proofs are valid against parent state root
//   - Full re-execution: replays transactions against witness-backed state (future)
package stateless

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state/witness"
	"github.com/n42blockchain/N42/params"
)

var (
	ErrNilBlock                = errors.New("stateless: nil block")
	ErrNilWitness              = errors.New("stateless: nil witness")
	ErrWitnessRootMismatch     = errors.New("stateless: witness parent root mismatch")
	ErrProofVerificationFailed = errors.New("stateless: proof verification failed")
	ErrInsufficientProofs      = errors.New("stateless: insufficient proofs in witness")
)

// Validator verifies blocks using only a witness (no state DB).
type Validator struct {
	chainConfig *params.ChainConfig
	codeCache   *CodeCache
}

// NewValidator creates a new stateless block Validator with a code cache
// for contract bytecodes that persist across blocks.
func NewValidator(config *params.ChainConfig) *Validator {
	return &Validator{
		chainConfig: config,
		codeCache:   NewCodeCache(4096),
	}
}

// ValidateBlock verifies a block using the accompanying witness.
// It verifies that all JMT Merkle proofs in the witness are valid against
// the witness's parent state root, and optionally caches contract codes.
//
// Returns nil if the block passes stateless validation.
func (v *Validator) ValidateBlock(blk block.IBlock, w *witness.BlockWitness) error {
	if blk == nil {
		return ErrNilBlock
	}
	if w == nil {
		return ErrNilWitness
	}

	if w.ParentRoot == (types.Hash{}) {
		return fmt.Errorf("%w: witness has zero parent root", ErrWitnessRootMismatch)
	}

	// Verify the witness has content (non-empty proofs)
	if len(w.AccountProofs) == 0 && len(w.StorageProofs) == 0 {
		return fmt.Errorf("%w: no account or storage proofs", ErrInsufficientProofs)
	}

	// Verify all JMT Merkle proofs against the parent state root
	if err := witness.VerifyWitness(w.ParentRoot, w); err != nil {
		return fmt.Errorf("%w: %v", ErrProofVerificationFailed, err)
	}

	// Cache contract codes from the witness for future blocks
	v.cacheWitnessCodes(w)

	log.Debug("Stateless validation passed",
		"block", blk.Number64(),
		"accountProofs", len(w.AccountProofs),
		"storageProofs", len(w.StorageProofs),
		"codes", len(w.Codes),
	)

	return nil
}

// ValidateBlockHeader performs lightweight header-only validation
// without a witness. Checks structural consistency.
func (v *Validator) ValidateBlockHeader(header *block.Header, parent *block.Header) error {
	if header == nil {
		return fmt.Errorf("nil header")
	}
	if parent == nil {
		return fmt.Errorf("nil parent header")
	}

	if header.ParentHash != parent.Hash() {
		return fmt.Errorf("parent hash mismatch: header.ParentHash=%x, parent.Hash=%x",
			header.ParentHash, parent.Hash())
	}

	parentNum := parent.Number.Uint64()
	headerNum := header.Number.Uint64()
	if headerNum != parentNum+1 {
		return fmt.Errorf("block number gap: parent=%d, header=%d", parentNum, headerNum)
	}

	if header.Time <= parent.Time {
		return fmt.Errorf("timestamp not increasing: parent=%d, header=%d", parent.Time, header.Time)
	}

	return nil
}

// GetCachedCode returns contract bytecode from the code cache.
// Used by the stateless EVM executor to resolve EXTCODECOPY/EXTCODESIZE
// without requiring the code in every block's witness.
func (v *Validator) GetCachedCode(codeHash types.Hash) ([]byte, bool) {
	return v.codeCache.Get(codeHash)
}

// CodeCacheLen returns the number of entries in the code cache.
func (v *Validator) CodeCacheLen() int {
	return v.codeCache.Len()
}

// ChainConfig returns the chain configuration used by this validator.
func (v *Validator) ChainConfig() *params.ChainConfig {
	return v.chainConfig
}

func (v *Validator) cacheWitnessCodes(w *witness.BlockWitness) {
	for _, hc := range w.Codes {
		if hc != nil && len(hc.Code) > 0 {
			v.codeCache.Put(hc.Hash, hc.Code)
		}
	}
}
