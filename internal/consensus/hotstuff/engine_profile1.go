// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Profile-1 mode of the consensus engine (standard A13, A14, E13, AM5,
// AM19, AM23): the vote gates and the certificate strictness that only a
// profile-1 chain applies.

package hotstuff

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

var (
	// ErrProfile1CertSet: a certificate's signer bitmap does not match the
	// fixed genesis set of a profile-1 chain (AM5, E13). There is no other
	// set to try.
	ErrProfile1CertSet = errors.New("profile 1: certificate does not match the genesis validator set")
	// ErrProfile1Domain: a profile-1 engine without the chain-bound H2-v4
	// identity; no certificate verifies (A14, fail closed).
	ErrProfile1Domain = errors.New("profile 1: H2-v4 signing identity not configured")
)

// SetProfile1 puts the engine in profile-1 mode with set, the chain's fixed
// genesis validator set. Call before Start.
func (e *ConsensusEngine) SetProfile1(set *ValidatorSet) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.p1Set = set
	// The two-phase gate is profile 1's only vote shape (A13).
	e.twoPhaseVote = set != nil || e.twoPhaseVote
}

// profile1 reports whether the engine runs profile-1 rules.
func (e *ConsensusEngine) profile1() bool { return e.p1Set != nil }

// profile1ExtendsGenesis is the extends rule for a proposal justified by the
// genesis QC (zero block hash) on profile 1: the block's parent must be the
// chain's genesis block. There is no fail-open: an unknown parent, or an
// engine without the chain-bound identity that names the genesis hash,
// refuses.
func (e *ConsensusEngine) profile1ExtendsGenesis(view ViewNumber, blockHash types.Hash) bool {
	if e.h2V4Identity == nil {
		log.Warn("profile 1: vote REFUSED, genesis hash unknown (H2-v4 identity not configured)",
			"view", view, "blockHash", blockHash)
		return false
	}
	genesis := e.h2V4Identity.GenesisHash
	parent, known := e.importedParents[blockHash]
	if !known || parent != genesis {
		log.Warn("profile 1: vote REFUSED, block justified by the genesis QC does not extend genesis",
			"view", view, "blockHash", blockHash, "blockParent", parent, "parentKnown", known, "genesis", genesis)
		return false
	}
	return true
}

// profile1CertError is the fail-closed pre-check of every certificate
// verification on profile 1: the H2-v4 identity is set and the bitmap is
// the genesis set's width. nil on other chains.
func (e *ConsensusEngine) profile1CertError(view ViewNumber, signers int) error {
	if e.p1Set == nil {
		return nil
	}
	if e.h2V4Identity == nil {
		return ErrProfile1Domain
	}
	if signers != int(e.p1Set.Len()) {
		return fmt.Errorf("%w: view %d bitmap %d, set %d", ErrProfile1CertSet, view, signers, e.p1Set.Len())
	}
	return nil
}
