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
//
// Fork choice rule. Compares two competing heads by total difficulty
// (pre-merge) or by PoS attestation weight, breaking ties on block
// time and hash. Used by BlockChain.reorg to decide whether a newly
// imported chain wins over the current canonical head.

package internal

import (
	"bytes"
	"errors"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// ChainReader defines a small collection of methods needed to access the local
// blockchain during header verification. It's implemented by both blockchain
// and lightchain.
type ChainReader interface {
	// Config retrieves the header chain's chain configuration.
	//Config() *params.ChainConfig

	// GetTd returns the total difficulty of a local block.
	GetTd(types.Hash, *uint256.Int) *uint256.Int
}

// ForkChoice is the fork chooser based on the highest total difficulty of the
// chain(the fork choice used in the eth1) and the external fork choice (the fork
// choice used in the eth2). This main goal of this ForkChoice is not only for
// offering fork choice during the eth1/2 merge phase, but also keep the compatibility
// for all other proof-of-work networks.
type ForkChoice struct {
	chain ChainReader

	// preserve is a helper function used in td fork choice.
	// Miners will prefer to choose the local mined block if the
	// local td is equal to the extern one. It can be nil for light
	// client
	preserve func(header block.IHeader) bool
}

func NewForkChoice(chainReader ChainReader, preserve func(header block.IHeader) bool) *ForkChoice {
	return &ForkChoice{
		chain:    chainReader,
		preserve: preserve,
	}
}

// ReorgNeeded returns whether the reorg should be applied
// based on the given external header and local canonical chain.
// In the td mode, the new head is chosen if the corresponding
// total difficulty is higher. In the extern mode, the trusted
// header is always selected as the head.
func (f *ForkChoice) ReorgNeeded(current block.IHeader, header block.IHeader) (bool, error) {
	currentNumber, err := requireHeaderNumber(current, "current header number unavailable")
	if err != nil {
		return false, err
	}
	headerNumber, err := requireHeaderNumber(header, "new header number unavailable")
	if err != nil {
		return false, err
	}
	var (
		localTD  = f.chain.GetTd(current.Hash(), currentNumber)
		externTd = f.chain.GetTd(header.Hash(), headerNumber)
	)
	if localTD == nil || externTd == nil {
		// Virtual-TD chains (PoS/HotStuff, difficulty≡1, TD rows elided) synthesize
		// TD from header presence, which can race tx visibility during an import.
		// Height is the fork-choice signal on those chains anyway (see the equal-TD
		// branch below), so fall back to a pure height comparison instead of
		// failing the import: strictly higher wins; an equal-height sibling does
		// NOT reorg here — under HotStuff the commit (CommitToCanonical) is the
		// canonicalization authority.
		if rawdb.VirtualTd {
			return headerNumber.Cmp(currentNumber) > 0, nil
		}
		log.Warnf("ForkChoice.ReorgNeeded: missing td, localTD=%v, externTd=%v, currentHash=%s, currentNum=%d, headerHash=%s, headerNum=%d",
			localTD, externTd, current.Hash().String(), currentNumber.Uint64(), header.Hash().String(), headerNumber.Uint64())
		return false, errors.New("missing td")
	}
	log.Tracef("ForkChoice.ReorgNeeded: localTD = %d, externTd = %d", localTD.Uint64(), externTd.Uint64())
	// Accept the new header as the chain head if the transition
	// is already triggered. We assume all the headers after the
	// transition come from the trusted consensus layer.
	//if ttd := f.chain.Config().TerminalTotalDifficulty; ttd != nil && ttd.Cmp(externTd.ToBig()) <= 0 {
	//	return true, nil
	//}
	// If the total difficulty is higher than our known, add it to the canonical chain
	// Second clause in the if statement reduces the vulnerability to selfish mining.
	// Please refer to http://www.cs.cornell.edu/~ie53/publications/btcProcFC.pdf
	reorg := externTd.Cmp(localTD) > 0
	if !reorg && externTd.Cmp(localTD) == 0 {
		number, headNumber := headerNumber.Uint64(), currentNumber.Uint64()
		if number < headNumber {
			reorg = true
		} else if number == headNumber {
			var currentPreserve, externPreserve bool
			if f.preserve != nil {
				currentPreserve, externPreserve = f.preserve(current), f.preserve(header)
			}
			// Deterministic tie-break: at equal height and equal TD, the lower
			// block hash wins on EVERY node. The previous misc.SecureFloat64()<0.5
			// coin flip made each node pick a same-height sibling at random, so a
			// competing pair never converged network-wide. Lower-hash-wins is
			// stable and identical across nodes; preserve (local-mined preference)
			// still overrides when set.
			reorg = !currentPreserve && (externPreserve ||
				bytes.Compare(header.Hash().Bytes(), current.Hash().Bytes()) < 0)
		} else {
			// number > headNumber at equal TD: a strictly higher block extends the
			// head. On a PoS/HotStuff chain with virtual (all-zero) TD, every block
			// has the same effective TD, so block height — not TD — is the
			// fork-choice signal. Without this, externTd = parentTd(0) + 1 is
			// constant across all live blocks and the canonical head never advances
			// past the first produced block.
			reorg = true
		}
	}
	return reorg, nil
}
