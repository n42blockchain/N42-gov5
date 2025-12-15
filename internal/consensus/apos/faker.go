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

package apos

import (
	"github.com/holiman/uint256"
	"github.com/ledgerwatch/erigon-lib/kv"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// Faker is a testing consensus engine that accepts all blocks as valid.
// It is useful for testing purposes where consensus validation should be bypassed.
type Faker struct{}

// NewFaker creates a new Faker consensus engine.
func NewFaker() consensus.Engine {
	return &Faker{}
}

func (f Faker) Author(header block.IHeader) (types.Address, error) {
	return header.(*block.Header).Coinbase, nil
}

func (f Faker) VerifyHeader(chain consensus.ChainHeaderReader, header block.IHeader, seal bool) error {
	// Faker accepts all headers as valid
	return nil
}

func (f Faker) VerifyHeaders(chain consensus.ChainHeaderReader, headers []block.IHeader, seals []bool) (chan<- struct{}, <-chan error) {
	abort := make(chan struct{})
	results := make(chan error, len(headers))
	go func() {
		for range headers {
			select {
			case <-abort:
				return
			case results <- nil:
			}
		}
	}()
	return abort, results
}

func (f Faker) VerifyUncles(chain consensus.ConsensusChainReader, blk block.IBlock) error {
	// Faker accepts all uncles as valid
	return nil
}

func (f Faker) Prepare(chain consensus.ChainHeaderReader, header block.IHeader) error {
	// No preparation needed for faker
	return nil
}

func (f Faker) Finalize(chain consensus.ChainHeaderReader, header block.IHeader, ibs *state.IntraBlockState, txs []*transaction.Transaction, uncles []block.IHeader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
	// Faker does not issue rewards
	return nil, nil, nil
}

func (f Faker) FinalizeAndAssemble(chain consensus.ChainHeaderReader, header block.IHeader, ibs *state.IntraBlockState, txs []*transaction.Transaction, uncles []block.IHeader, receipts []*block.Receipt) (block.IBlock, []*block.Reward, map[types.Address]*uint256.Int, error) {
	return block.NewBlock(header, txs), nil, nil, nil
}

func (f Faker) Rewards(tx kv.RwTx, header block.IHeader, ibs *state.IntraBlockState, setRewards bool) ([]*block.Reward, error) {
	// Faker does not issue rewards
	return nil, nil
}

func (f Faker) Seal(chain consensus.ChainHeaderReader, blk block.IBlock, results chan<- block.IBlock, stop <-chan struct{}) error {
	// Faker immediately returns the block without sealing
	select {
	case results <- blk:
	case <-stop:
	}
	return nil
}

func (f Faker) SealHash(header block.IHeader) types.Hash {
	return header.Hash()
}

func (f Faker) CalcDifficulty(chain consensus.ChainHeaderReader, time uint64, parent block.IHeader) *uint256.Int {
	return uint256.NewInt(1)
}

func (f Faker) Type() params.ConsensusType {
	return params.Faker
}

func (f Faker) APIs(chain consensus.ConsensusChainReader) []jsonrpc.API {
	return nil
}

func (f Faker) Close() error {
	return nil
}

func (f Faker) IsServiceTransaction(sender types.Address, syscall consensus.SystemCall) bool {
	return false
}
