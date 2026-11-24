// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The header half of the profile-1 Round-1 vote gate (standard A13, E6,
// AM19, AM23) and the n42_blocks_refused_total{reason} counter (J2).

package hotstuff

import (
	"errors"
	"fmt"
	"time"

	prometheus "github.com/n42blockchain/N42/common/metrics"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/log"
)

// profile1RefusalReasons labels n42_blocks_refused_total by the static rule
// that refused the block.
var profile1RefusalReasons = []struct {
	err    error
	reason string
}{
	{ErrProfile1Extra, "extra"},
	{ErrProfile1QC, "header_qc"},
	{ErrProfile1Seal, "seal"},
	{ErrProfile1Proposer, "proposer"},
	{ErrProfile1Difficulty, "difficulty"},
	{ErrProfile1Nonce, "nonce"},
	{ErrProfile1MixHash, "mix_hash"},
	{ErrProfile1OmmersHash, "ommers_hash"},
	{ErrProfile1TimeBeforeParent, "time_before_parent"},
	{ErrProfile1TimeFuture, "time_future"},
	{ErrProfile1WithdrawalsRoot, "withdrawals_root"},
	{ErrProfile1RequestsHash, "requests_hash"},
	{ErrProfile1BlobGas, "blob_gas"},
	{ErrProfile1Element22, "element22"},
	{ErrProfile1GasLimit, "gas_limit"},
	{ErrProfile1BaseFee, "base_fee"},
}

// Profile1RefusalReason is the n42_blocks_refused_total label of a header
// refusal; "header" for a rule shared with other chains.
func Profile1RefusalReason(err error) string {
	for _, r := range profile1RefusalReasons {
		if errors.Is(err, r.err) {
			return r.reason
		}
	}
	return "header"
}

// VoteGateHeaderCheck is the header half of the profile-1 Round-1 gate,
// called by the deferred-execution proposal check: StaticHeaderCheck with
// the clock now. A refusal (not a missing parent) is logged with its
// ErrProfile1* reason and counted in n42_blocks_refused_total{reason} (J2).
func (h *HotStuff) VoteGateHeaderCheck(chain consensus.ChainHeaderReader, header *block.Header, now time.Time) error {
	err := h.StaticHeaderCheck(chain, header, now)
	if err == nil || errors.Is(err, consensus.ErrUnknownAncestor) {
		return err
	}
	reason := Profile1RefusalReason(err)
	prometheus.GetOrCreateCounter(fmt.Sprintf(`n42_blocks_refused_total{reason=%q}`, reason)).Inc()
	log.Warn("profile 1: block refused at the vote gate, not voting", "number", header.Number, "hash", header.Hash(), "reason", reason, "err", err)
	return err
}
