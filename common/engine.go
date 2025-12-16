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

package common

import (
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// ChainHeaderReader defines a small collection of methods needed to access the local
// blockchain during header verification. This is the common layer definition.
// The actual implementation is in internal/consensus.
type ChainHeaderReader interface {
	// Config retrieves the blockchain's chain configuration.
	Config() *params.ChainConfig

	// CurrentBlock retrieves the current block from the local chain.
	CurrentBlock() block.IBlock

	// GetHeader retrieves a block header from the database by hash and number.
	GetHeader(hash types.Hash, number *uint256.Int) block.IHeader

	// GetHeaderByNumber retrieves a block header from the database by number.
	GetHeaderByNumber(number *uint256.Int) block.IHeader

	// GetHeaderByHash retrieves a block header from the database by its hash.
	GetHeaderByHash(hash types.Hash) (block.IHeader, error)

	// GetTd retrieves the total difficulty from the database by hash and number.
	GetTd(types.Hash, *uint256.Int) *uint256.Int

	// GetBlockByNumber retrieves a block from the database by number.
	GetBlockByNumber(number *uint256.Int) (block.IBlock, error)

	// GetDepositInfo retrieves deposit information for an address.
	GetDepositInfo(address types.Address) (*uint256.Int, *uint256.Int)

	// GetAccountRewardUnpaid retrieves unpaid reward for an account.
	GetAccountRewardUnpaid(account types.Address) (*uint256.Int, error)
}

// ConsensusEngine is the common layer interface for consensus engines.
// The actual implementations (apoa, apos) are in internal/consensus.
type ConsensusEngine interface {
	// Author retrieves the address of the account that minted the given block.
	Author(header block.IHeader) (types.Address, error)

	// VerifyHeader checks whether a header conforms to the consensus rules.
	VerifyHeader(chain ChainHeaderReader, header block.IHeader, seal bool) error

	// VerifyHeaders is similar to VerifyHeader, but verifies a batch of headers concurrently.
	VerifyHeaders(chain ChainHeaderReader, headers []block.IHeader, seals []bool) (chan<- struct{}, <-chan error)

	// Prepare initializes the consensus fields of a block header.
	Prepare(chain ChainHeaderReader, header block.IHeader) error

	// Seal generates a new sealing request for the given input block.
	Seal(chain ChainHeaderReader, block block.IBlock, results chan<- block.IBlock, stop <-chan struct{}) error

	// SealHash returns the hash of a block prior to it being sealed.
	SealHash(header block.IHeader) types.Hash

	// CalcDifficulty is the difficulty adjustment algorithm.
	CalcDifficulty(chain ChainHeaderReader, time uint64, parent block.IHeader) *uint256.Int

	// Type returns the consensus type.
	Type() params.ConsensusType

	// Close terminates any background threads maintained by the consensus engine.
	Close() error
}

