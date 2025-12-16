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

package internal

// =============================================================================
// BlockChain Reader Methods
// =============================================================================
//
// This file contains read-only query methods for the BlockChain.
// These methods do not modify blockchain state and are safe for concurrent access.
//
// Method categories:
//   - Chain configuration: Config, Engine, DB
//   - Block access: GetBlock*, HasBlock, CurrentBlock, GenesisBlock
//   - Header access: GetHeader*, GetCanonicalHash, GetBlockNumber, GetTd
//   - Receipt/Log access: GetReceipts, GetLogs
//   - State access: StateAt, HasState, HasBlockAndState
//   - Deposit/Reward: GetDepositInfo, GetAccountRewardUnpaid
//   - Lifecycle: Quit

import (
	"github.com/holiman/uint256"
	"github.com/ledgerwatch/erigon-lib/kv"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/contracts/deposit"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Chain Configuration Access
// =============================================================================

// Config returns the chain configuration.
func (bc *BlockChain) Config() *params.ChainConfig {
	return bc.chainConfig
}

// Engine returns the consensus engine.
// Returns interface{} to avoid circular dependency with consensus package.
func (bc *BlockChain) Engine() interface{} {
	return bc.engine
}

// DB returns the underlying database.
func (bc *BlockChain) DB() kv.RwDB {
	return bc.ChainDB
}

// =============================================================================
// Block Access
// =============================================================================

// CurrentBlock returns the current head block.
func (bc *BlockChain) CurrentBlock() block.IBlock {
	return bc.currentBlock.Load()
}

// GenesisBlock returns the genesis block.
func (bc *BlockChain) GenesisBlock() block.IBlock {
	return bc.genesisBlock
}

// Blocks returns all cached blocks.
func (bc *BlockChain) Blocks() []block.IBlock {
	return bc.blocks
}

// GetBlock retrieves a block from the database by hash and number.
// Returns nil if the block is not found.
func (bc *BlockChain) GetBlock(hash types.Hash, number uint64) block.IBlock {
	if hash == (types.Hash{}) {
		return nil
	}

	if blk, ok := bc.blockCache.Get(hash); ok {
		return blk
	}

	tx, err := bc.ChainDB.BeginRo(bc.ctx)
	if nil != err {
		return nil
	}
	defer tx.Rollback()
	blk := rawdb.ReadBlock(tx, hash, number)
	if blk == nil {
		return nil
	}
	bc.blockCache.Add(hash, blk)
	return blk
}

// GetBlockByHash retrieves a block by its hash.
func (bc *BlockChain) GetBlockByHash(h types.Hash) (block.IBlock, error) {
	number := bc.GetBlockNumber(h)
	if nil == number {
		return nil, errBlockDoesNotExist
	}
	return bc.GetBlock(h, *number), nil
}

// GetBlockByNumber retrieves a block by its number.
func (bc *BlockChain) GetBlockByNumber(number *uint256.Int) (block.IBlock, error) {
	var hash types.Hash
	bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		hash, _ = rawdb.ReadCanonicalHash(tx, number.Uint64())
		return nil
	})

	if hash == (types.Hash{}) {
		return nil, nil
	}
	return bc.GetBlock(hash, number.Uint64()), nil
}

// GetBlocksFromHash retrieves a number of blocks starting from a given hash,
// going backwards (towards genesis).
func (bc *BlockChain) GetBlocksFromHash(hash types.Hash, n int) (blocks []block.IBlock) {
	var number *uint64
	if num, ok := bc.numberCache.Get(hash); ok {
		number = &num
	} else {
		bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
			number = rawdb.ReadHeaderNumber(tx, hash)
			return nil
		})
		if number == nil {
			return nil
		}
		bc.numberCache.Add(hash, *number)
	}

	for i := 0; i < n; i++ {
		blk := bc.GetBlock(hash, *number)
		if blk == nil {
			break
		}

		blocks = append(blocks, blk)
		hash = blk.ParentHash()
		*number--
	}
	return blocks
}

// HasBlock checks if a block exists in the database.
func (bc *BlockChain) HasBlock(hash types.Hash, number uint64) bool {
	var flag bool
	if bc.blockCache.Contains(hash) {
		return true
	}

	bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		flag = rawdb.HasHeader(tx, hash, number)
		return nil
	})

	return flag
}

// =============================================================================
// Header Access
// =============================================================================

// GetHeader retrieves a block header by hash and number.
func (bc *BlockChain) GetHeader(h types.Hash, number *uint256.Int) block.IHeader {
	// Short circuit if the header's already in the cache, retrieve otherwise
	if header, ok := bc.headerCache.Get(h); ok {
		return header
	}

	tx, err := bc.ChainDB.BeginRo(bc.ctx)
	if nil != err {
		return nil
	}
	defer tx.Rollback()
	header := rawdb.ReadHeader(tx, h, number.Uint64())
	if nil == header {
		return nil
	}

	bc.headerCache.Add(h, header)
	return header
}

// GetHeaderByNumber retrieves a block header by number.
func (bc *BlockChain) GetHeaderByNumber(number *uint256.Int) block.IHeader {
	tx, err := bc.ChainDB.BeginRo(bc.ctx)
	if nil != err {
		log.Error("cannot open chain db", "err", err)
		return nil
	}
	defer tx.Rollback()

	hash, err := rawdb.ReadCanonicalHash(tx, number.Uint64())
	if nil != err {
		log.Error("cannot open chain db", "err", err)
		return nil
	}
	if hash == (types.Hash{}) {
		return nil
	}

	if header, ok := bc.headerCache.Get(hash); ok {
		return header
	}
	header := rawdb.ReadHeader(tx, hash, number.Uint64())
	if nil == header {
		return nil
	}
	bc.headerCache.Add(hash, header)
	return header
}

// GetHeaderByHash retrieves a block header by hash.
func (bc *BlockChain) GetHeaderByHash(h types.Hash) (block.IHeader, error) {
	number := bc.GetBlockNumber(h)
	if number == nil {
		return nil, nil
	}

	return bc.GetHeader(h, uint256.NewInt(*number)), nil
}

// GetCanonicalHash returns the canonical hash for a given block number.
func (bc *BlockChain) GetCanonicalHash(number *uint256.Int) types.Hash {
	tx, err := bc.ChainDB.BeginRo(bc.ctx)
	if nil != err {
		return types.Hash{}
	}
	defer tx.Rollback()

	hash, err := rawdb.ReadCanonicalHash(tx, number.Uint64())
	if nil != err {
		return types.Hash{}
	}
	return hash
}

// GetBlockNumber retrieves the block number for a given hash.
func (bc *BlockChain) GetBlockNumber(hash types.Hash) *uint64 {
	if cached, ok := bc.numberCache.Get(hash); ok {
		return &cached
	}
	tx, err := bc.ChainDB.BeginRo(bc.ctx)
	if nil != err {
		return nil
	}
	defer tx.Rollback()
	number := rawdb.ReadHeaderNumber(tx, hash)
	if number != nil {
		bc.numberCache.Add(hash, *number)
	}
	return number
}

// GetTd retrieves the total difficulty for a block.
func (bc *BlockChain) GetTd(hash types.Hash, number *uint256.Int) *uint256.Int {
	if td, ok := bc.tdCache.Get(hash); ok {
		return td
	}

	var td *uint256.Int
	_ = bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		ptd, err := rawdb.ReadTd(tx, hash, number.Uint64())
		if nil != err {
			return err
		}
		td = ptd
		return nil
	})

	bc.tdCache.Add(hash, td)
	return td
}

// =============================================================================
// Receipt and Log Access
// =============================================================================

// GetReceipts retrieves receipts for a block by hash.
func (bc *BlockChain) GetReceipts(blockHash types.Hash) (block.Receipts, error) {
	rtx, err := bc.ChainDB.BeginRo(bc.ctx)
	if err != nil {
		return nil, err
	}
	defer rtx.Rollback()
	return rawdb.ReadReceiptsByHash(rtx, blockHash)
}

// GetLogs retrieves all logs for a block by hash.
func (bc *BlockChain) GetLogs(blockHash types.Hash) ([][]*block.Log, error) {
	receipts, err := bc.GetReceipts(blockHash)
	if err != nil {
		return nil, err
	}

	logs := make([][]*block.Log, len(receipts))
	for i, receipt := range receipts {
		logs[i] = receipt.Logs
	}
	return logs, nil
}

// =============================================================================
// State Access
// =============================================================================

// StateAt returns a new state at the given block number.
// Returns interface{} to avoid circular dependency.
func (bc *BlockChain) StateAt(tx kv.Tx, blockNr uint64) interface{} {
	reader := state.NewPlainState(tx, blockNr+1)
	return state.New(reader)
}

// HasState checks if the state for a block exists.
func (bc *BlockChain) HasState(hash types.Hash) bool {
	tx, err := bc.ChainDB.BeginRo(bc.ctx)
	if nil != err {
		return false
	}
	defer tx.Rollback()
	is, err := rawdb.IsCanonicalHash(tx, hash)
	if nil != err {
		return false
	}
	return is
}

// HasBlockAndState checks if a block and its state exist.
func (bc *BlockChain) HasBlockAndState(hash types.Hash, number uint64) bool {
	blk := bc.GetBlock(hash, number)
	if blk == nil {
		return false
	}
	return bc.HasState(blk.Hash())
}

// =============================================================================
// Deposit and Reward Access
// =============================================================================

// GetDepositInfo retrieves deposit information for an address.
func (bc *BlockChain) GetDepositInfo(address types.Address) (*uint256.Int, *uint256.Int) {
	var info *deposit.Info
	bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		info = deposit.GetDepositInfo(tx, address)
		return nil
	})
	if nil == info {
		return nil, nil
	}
	return info.RewardPerBlock, info.MaxRewardPerEpoch
}

// GetAccountRewardUnpaid retrieves unpaid rewards for an account.
func (bc *BlockChain) GetAccountRewardUnpaid(account types.Address) (*uint256.Int, error) {
	var value *uint256.Int
	var err error
	bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		value, err = rawdb.GetAccountReward(tx, account)
		return nil
	})
	return value, err
}

// =============================================================================
// Lifecycle
// =============================================================================

// Quit returns a channel that is closed when the blockchain is stopping.
func (bc *BlockChain) Quit() <-chan struct{} {
	return bc.ctx.Done()
}

