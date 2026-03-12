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

package mev

import (
	"context"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"go.uber.org/zap"
)

// Auction manages the block auction process, comparing locally built blocks
// against bids from external builders through MEV-Boost relays.
// The auction ensures that the most profitable block is selected for proposal,
// subject to a configurable minimum bid threshold.
type Auction struct {
	relay     *RelayClient
	minBidWei *uint256.Int // minimum bid to accept a relay block over local
	logger    *zap.Logger
}

// NewAuction creates a new Auction instance.
// If minBid is nil, it defaults to zero (accept any profitable relay bid).
func NewAuction(relay *RelayClient, minBid *uint256.Int, logger *zap.Logger) *Auction {
	if minBid == nil {
		minBid = new(uint256.Int)
	}
	return &Auction{
		relay:     relay,
		minBidWei: minBid,
		logger:    logger,
	}
}

// CompareAndSelect determines whether the relay bid should be used instead of
// the locally built block. A relay bid wins if:
//  1. The bid value exceeds the minimum bid threshold (minBidWei).
//  2. The bid value is strictly greater than the local block value.
//
// This prevents accepting relay blocks that offer negligible improvement
// and provides a floor for relay block profitability.
func (a *Auction) CompareAndSelect(localValue *uint256.Int, relayBid *BuilderBid) (useRelay bool) {
	if relayBid == nil || relayBid.Value == nil {
		a.logger.Debug("no relay bid to compare, using local block")
		return false
	}

	if localValue == nil {
		localValue = new(uint256.Int)
	}

	// Check minimum bid threshold first.
	if relayBid.Value.Cmp(a.minBidWei) < 0 {
		a.logger.Debug("relay bid below minimum threshold",
			zap.String("bid_value", relayBid.Value.String()),
			zap.String("min_bid", a.minBidWei.String()),
		)
		return false
	}

	// Relay bid must strictly exceed local block value.
	if relayBid.Value.Cmp(localValue) <= 0 {
		a.logger.Debug("local block value >= relay bid, using local block",
			zap.String("local_value", localValue.String()),
			zap.String("relay_value", relayBid.Value.String()),
		)
		return false
	}

	a.logger.Info("relay bid wins auction",
		zap.String("local_value", localValue.String()),
		zap.String("relay_value", relayBid.Value.String()),
		zap.String("relay", relayBid.RelayURL),
	)
	return true
}

// RunAuction performs a complete auction round: it fetches the best bid from
// configured relays and compares it against the local block value.
//
// Returns whether to use the relay block and the winning bid (if any).
// On relay errors, it falls back gracefully to the local block.
func (a *Auction) RunAuction(ctx context.Context, slot uint64, parentHash types.Hash, localValue *uint256.Int) (useRelay bool, bid *BuilderBid) {
	if a.relay == nil {
		a.logger.Debug("no relay configured, using local block")
		return false, nil
	}

	pubkey := a.relay.ValidatorPubkey()

	if len(pubkey) == 0 {
		a.logger.Warn("validator public key not set, cannot run auction")
		return false, nil
	}

	relayBid, err := a.relay.GetHeader(ctx, slot, parentHash, pubkey)
	if err != nil {
		a.logger.Warn("failed to get header from relay, falling back to local block",
			zap.Uint64("slot", slot),
			zap.Error(err),
		)
		return false, nil
	}

	if a.CompareAndSelect(localValue, relayBid) {
		return true, relayBid
	}

	return false, nil
}
