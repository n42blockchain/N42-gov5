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
// EVM context adapters. Builds vm.BlockContext and vm.TxContext
// snapshots from a BlockChain + header pair so the EVM sees a
// consistent view of block number, base fee, coinbase and
// difficulty, honouring EIP-1559 and EIP-3198 rules.

package internal

import (
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/params"
	"math/big"
)

// NewEVMBlockContext creates a new context for use in the EVM.
func NewEVMBlockContext(header *block.Header, blockHashFunc func(n uint64) types.Hash, engine consensus.Engine, chainCfg *params.ChainConfig, author *types.Address) evmtypes.BlockContext {
	// If we don't have an explicit author (i.e. not mining), extract from the header
	var beneficiary types.Address
	if author == nil {
		if engine != nil {
			beneficiary, _ = engine.Author(header) // Ignore error, we're past header validation
		}
		if beneficiary == (types.Address{}) {
			beneficiary = header.Coinbase
		}
	} else {
		beneficiary = *author
	}
	var baseFee uint256.Int
	if header.BaseFee != nil {
		baseFee.SetFromBig(header.BaseFee.ToBig())
	}
	excessBlobGas := uint64(0)
	if header.ExcessBlobGas != nil {
		excessBlobGas = *header.ExcessBlobGas
	}
	blobBaseFee := transaction.CalcBlobFee(excessBlobGas)
	if chainCfg != nil {
		blobBaseFee = chainCfg.CalcBlobFee(excessBlobGas, header.Time)
	}
	difficulty := new(big.Int)
	if header.Difficulty != nil {
		difficulty = header.Difficulty.ToBig()
	}

	var prevRandDao *types.Hash
	if header.Difficulty != nil && header.Difficulty.Cmp(uint256.NewInt(0)) == 0 {
		// EIP-4399. We use SerenityDifficulty (i.e. 0) as a telltale of Proof-of-Stake blocks.
		prevRandDao = &header.MixDigest
	}

	consensusType := params.ConsensusType("")
	if engine != nil {
		consensusType = engine.Type()
	}
	policy := newExecutionChainPolicy(nil, consensusType)
	headerNumber := uint64(0)
	if number, err := requireHeaderNumber(header, "header number unavailable"); err == nil {
		headerNumber = number.Uint64()
	}

	return evmtypes.BlockContext{
		CanTransfer:   CanTransfer,
		Transfer:      policy.transferFunc(),
		GetHash:       blockHashFunc,
		Coinbase:      beneficiary,
		BlockNumber:   headerNumber,
		Time:          header.Time,
		Difficulty:    difficulty,
		BaseFee:       &baseFee,
		BlobBaseFee:   blobBaseFee,
		ExcessBlobGas: excessBlobGas,
		GasLimit:      header.GasLimit,
		PrevRanDao:    prevRandDao,
	}
}

// NewEVMTxContext creates a new transaction context for a single transaction.
func NewEVMTxContext(msg Message) evmtypes.TxContext {
	var gasPrice *uint256.Int
	if direct, ok := msg.(directExecutionValues); ok {
		gasPrice, _, _, _ = direct.ExecutionValues()
	} else {
		gasPrice = msg.GasPrice()
	}
	return evmtypes.TxContext{
		Origin:     msg.From(),
		GasPrice:   gasPrice,
		BlobHashes: append([]types.Hash(nil), msg.BlobHashes()...),
	}
}

// GetHashFn returns a GetHashFunc which retrieves header hashes by number
func GetHashFn(ref *block.Header, getHeader func(hash types.Hash, number uint64) *block.Header) func(n uint64) types.Hash {
	// Cache will initially contain [refHash.parent],
	// Then fill up with [refHash.p, refHash.pp, refHash.ppp, ...]
	var cache []types.Hash

	return func(n uint64) types.Hash {
		refNumber, err := requireHeaderNumber(ref, "")
		if err != nil || n >= refNumber.Uint64() {
			return types.Hash{}
		}
		// If there's no hash cache yet, make one
		if len(cache) == 0 {
			cache = append(cache, ref.ParentHash)
		}
		if idx := refNumber.Uint64() - n - 1; idx < uint64(len(cache)) {
			return cache[idx]
		}
		if getHeader == nil {
			return types.Hash{}
		}
		// No luck in the cache, but we can start iterating from the last element we already know
		lastKnownHash := cache[len(cache)-1]
		lastKnownNumber := refNumber.Uint64() - uint64(len(cache))

		for {
			header := getHeader(lastKnownHash, lastKnownNumber)
			if header == nil {
				break
			}
			headerNumber, err := requireHeaderNumber(header, "")
			if err != nil || headerNumber.Uint64() == 0 {
				break
			}
			cache = append(cache, header.ParentHash)
			lastKnownHash = header.ParentHash
			lastKnownNumber = headerNumber.Uint64() - 1
			if n == lastKnownNumber {
				return lastKnownHash
			}
		}
		return types.Hash{}
	}
}

// CanTransfer checks whether there are enough funds in the address' account to make a transfer.
// This does not take the necessary gas in to account to make the transfer valid.
func CanTransfer(db evmtypes.IntraBlockState, addr types.Address, amount *uint256.Int) bool {
	return !db.GetBalance(addr).Lt(amount)
}

// Transfer subtracts amount from sender and adds amount to recipient using the given Db
func Transfer(db evmtypes.IntraBlockState, sender, recipient types.Address, amount *uint256.Int, bailout bool) {
	if !bailout {
		db.SubBalance(sender, amount)
	}
	db.AddBalance(recipient, amount)
}

// BorTransfer transfer in Bor
func BorTransfer(db evmtypes.IntraBlockState, sender, recipient types.Address, amount *uint256.Int, bailout bool) {
	// get inputs before
	//input1 := db.GetBalance(sender).Clone()
	//input2 := db.GetBalance(recipient).Clone()

	if !bailout {
		db.SubBalance(sender, amount)
	}
	db.AddBalance(recipient, amount)

	//// get outputs after
	//output1 := db.GetBalance(sender).Clone()
	//output2 := db.GetBalance(recipient).Clone()
	//
	//// add transfer log
	//AddTransferLog(db, sender, recipient, amount, input1, input2, output1, output2)
}

// ChainContext supports retrieving headers and consensus parameters from the
// current blockchain to be used during transaction processing.
type ChainContext interface {
	// Engine retrieves the chain's consensus engine.
	Engine() consensus.Engine

	// GetHeader returns the header corresponding to the hash/number argument pair.
	GetHeader(types.Hash, uint64) *block.Header
}
