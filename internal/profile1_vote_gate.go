// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"errors"
	"fmt"
	"time"

	prometheus "github.com/n42blockchain/N42/common/metrics"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/consensus"
)

// ErrProfile1NoHeaderCheck: a profile-1 chain runs an engine that cannot
// apply the vote-time header check, so no block may pass the Round-1 gate.
var ErrProfile1NoHeaderCheck = errors.New("profile 1: the consensus engine has no vote-time header check")

// ErrProfile1DeferredCheck wraps a profile-1 refusal by the deferred-execution
// proposal check (carried results or includability), as distinct from the
// static header rules.
var ErrProfile1DeferredCheck = errors.New("profile 1: deferred-execution proposal check failed")

// voteHeaderChecker is the consensus engine's half of the Round-1 gate: the
// static header rules plus the vote-time clock bound (B4). The HotStuff
// engine implements it; internal cannot import it.
type voteHeaderChecker interface {
	VoteGateHeaderCheck(chain consensus.ChainHeaderReader, header *block.Header, now time.Time) error
}

// profile1VoteHeaderCheck runs the engine's vote-time header check on hdr
// (A13, E6, AM19, AM23). The engine counts and labels its own refusals.
// retry reports a header whose parent is not stored yet.
func (bc *BlockChain) profile1VoteHeaderCheck(hdr *block.Header) (retry bool, err error) {
	checker, ok := bc.engine.(voteHeaderChecker)
	if !ok {
		countProfile1Refusal("no_header_check")
		return false, ErrProfile1NoHeaderCheck
	}
	if err := checker.VoteGateHeaderCheck(bc, hdr, time.Now()); err != nil {
		return errors.Is(err, consensus.ErrUnknownAncestor), err
	}
	return false, nil
}

// profile1DeferredRefusal labels and counts a final (non-retry) refusal of
// the deferred-execution check on a profile-1 chain.
func profile1DeferredRefusal(reason string, err error) error {
	countProfile1Refusal(reason)
	return fmt.Errorf("%w (%s): %w", ErrProfile1DeferredCheck, reason, err)
}

// countProfile1Refusal increments n42_blocks_refused_total{reason} (J2). The
// consensus engine counts the header reasons under the same family.
func countProfile1Refusal(reason string) {
	prometheus.GetOrCreateCounter(fmt.Sprintf(`n42_blocks_refused_total{reason=%q}`, reason)).Inc()
}
