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

package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/accounts"
	gaspool "github.com/n42blockchain/N42/common"
	block "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	rpc "github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// ChainConfig returns the active chain configuration.
func (b *API) ChainConfig() *params.ChainConfig {
	return b.chainConfig
}

// CurrentBlock returns the current block header, or nil if unavailable.
func (b *API) CurrentBlock() *block.Header {
	blk := b.bc.CurrentBlock()
	if blk == nil {
		return nil
	}
	h := blk.Header()
	if h == nil {
		return nil
	}
	header, ok := h.(*block.Header)
	if !ok {
		return nil
	}
	return header
}

func (b *API) canonicalHashForNumber(number *uint256.Int) (types.Hash, error) {
	chain, ok := b.bc.(*internal.BlockChain)
	if !ok {
		return types.Hash{}, errors.New("canonical hash check unavailable for this blockchain implementation")
	}
	return chain.GetCanonicalHash(number), nil
}

// HeaderByNumber returns the block header for the given block number.
// Returns the latest header when rpc.LatestBlockNumber is specified.
func (b *API) HeaderByNumber(ctx context.Context, number rpc.BlockNumber) (*block.Header, error) {
	if number == rpc.LatestBlockNumber {
		header := b.CurrentBlock()
		if header == nil {
			return nil, errors.New("current block not available")
		}
		return header, nil
	}

	iHeader := b.bc.GetHeaderByNumber(uint256.NewInt(uint64(number.Int64())))
	if iHeader == nil {
		return nil, nil
	}
	header, ok := iHeader.(*block.Header)
	if !ok {
		return nil, errors.New("unexpected header type")
	}
	return header, nil
}

// HeaderByNumberOrHash returns the block header for the given block number or hash.
func (b *API) HeaderByNumberOrHash(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (*block.Header, error) {
	if blockNr, ok := blockNrOrHash.Number(); ok {
		return b.HeaderByNumber(ctx, blockNr)
	}
	if hash, ok := blockNrOrHash.Hash(); ok {
		header, _ := b.bc.GetHeaderByHash(hash)
		if header == nil {
			return nil, errors.New("header for hash not found")
		}
		if blockNrOrHash.RequireCanonical {
			canonicalHash, err := b.canonicalHashForNumber(header.Number64())
			if err != nil {
				return nil, err
			}
			if canonicalHash != hash {
				return nil, errors.New("hash is not currently canonical")
			}
		}
		h, ok := header.(*block.Header)
		if !ok {
			return nil, errors.New("unexpected header type")
		}
		return h, nil
	}
	return nil, errors.New("invalid arguments; neither block nor hash specified")
}

// HeaderByHash returns the block header for the given hash.
func (b *API) HeaderByHash(ctx context.Context, hash types.Hash) (*block.Header, error) {
	iHeader, _ := b.bc.GetHeaderByHash(hash)
	if iHeader == nil {
		return nil, nil
	}
	h, ok := iHeader.(*block.Header)
	if !ok {
		return nil, errors.New("unexpected header type")
	}
	return h, nil
}

// BlockByNumber returns the block for the given block number.
// Returns the latest block when rpc.LatestBlockNumber is specified.
func (b *API) BlockByNumber(ctx context.Context, number rpc.BlockNumber) (*block.Block, error) {
	if number == rpc.LatestBlockNumber {
		header := b.bc.CurrentBlock()
		if header == nil {
			return nil, errors.New("current block not available")
		}
		headerNumber, err := requireUint256(header.Number64(), "current block number unavailable")
		if err != nil {
			return nil, err
		}
		iBlock := b.bc.GetBlock(header.Hash(), headerNumber.Uint64())
		if iBlock == nil {
			return nil, errors.New("current block body not found")
		}
		blk, ok := iBlock.(*block.Block)
		if !ok {
			return nil, errors.New("unexpected block type")
		}
		return blk, nil
	}

	iBlock, err := b.bc.GetBlockByNumber(uint256.NewInt(uint64(number)))
	if err != nil {
		return nil, err
	}
	if iBlock == nil {
		return nil, nil
	}
	blk, ok := iBlock.(*block.Block)
	if !ok {
		return nil, errors.New("unexpected block type")
	}
	return blk, nil
}

// BlockByHash returns the block for the given hash.
func (b *API) BlockByHash(ctx context.Context, hash types.Hash) (*block.Block, error) {
	iBlock, _ := b.bc.GetBlockByHash(hash)
	if iBlock == nil {
		return nil, nil
	}
	blk, ok := iBlock.(*block.Block)
	if !ok {
		return nil, errors.New("unexpected block type")
	}
	return blk, nil
}

// BlockByNumberOrHash returns the block for the given block number or hash.
func (b *API) BlockByNumberOrHash(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (*block.Block, error) {
	if blockNr, ok := blockNrOrHash.Number(); ok {
		return b.BlockByNumber(ctx, blockNr)
	}
	if hash, ok := blockNrOrHash.Hash(); ok {
		header, _ := b.bc.GetHeaderByHash(hash)
		if header == nil {
			return nil, errors.New("header for hash not found")
		}
		headerNumber, err := requireUint256(header.Number64(), "header number unavailable")
		if err != nil {
			return nil, err
		}
		if blockNrOrHash.RequireCanonical {
			canonicalHash, err := b.canonicalHashForNumber(headerNumber)
			if err != nil {
				return nil, err
			}
			if canonicalHash != hash {
				return nil, errors.New("hash is not currently canonical")
			}
		}
		iBlock := b.bc.GetBlock(hash, headerNumber.Uint64())
		if iBlock == nil {
			return nil, errors.New("header found, but block body is missing")
		}
		blk, ok := iBlock.(*block.Block)
		if !ok {
			return nil, errors.New("unexpected block type")
		}
		return blk, nil
	}
	return nil, errors.New("invalid arguments; neither block nor hash specified")
}

// GetReceipts returns the receipts for the given block hash.
func (b *API) GetReceipts(ctx context.Context, hash types.Hash) (block.Receipts, error) {
	return b.bc.GetReceipts(hash)
}

// GetTd returns the total difficulty for the block with the given hash.
func (b *API) GetTd(ctx context.Context, hash types.Hash) *uint256.Int {
	if header, _ := b.bc.GetHeaderByHash(hash); header != nil {
		return b.bc.GetTd(hash, header.Number64())
	}
	return nil
}

// GetEVM creates a new EVM instance configured for the given message and block header.
func (b *API) GetEVM(ctx context.Context, msg *transaction.Message, ibs *state.IntraBlockState, header *block.Header, vmConfig *vm.Config) (*vm.EVM, func() error, error) {
	if vmConfig == nil {
		vmConfig = &vm.Config{}
	}
	txContext := internal.NewEVMTxContext(msg)
	getHeader := func(hash types.Hash, n uint64) *block.Header {
		h := b.bc.GetHeader(hash, uint256.NewInt(n))
		if h == nil {
			return nil
		}
		th, ok := h.(*block.Header)
		if !ok {
			return nil
		}
		return th
	}
	engine, ok := b.bc.Engine().(consensus.Engine)
	if !ok {
		return nil, nil, errors.New("consensus engine not available")
	}
	blockCtx := internal.NewEVMBlockContext(header, internal.GetHashFn(header, getHeader), engine, nil)
	return vm.NewEVM(blockCtx, txContext, ibs, b.bc.Config(), *vmConfig), ibs.Error, nil
}

// GetTransaction returns the transaction, block hash, block number, and tx index
// for the given transaction hash.
func (b *API) GetTransaction(ctx context.Context, txHash types.Hash) (*transaction.Transaction, types.Hash, uint64, uint64, error) {
	var (
		t           *transaction.Transaction
		blockHash   types.Hash
		blockNumber uint64
		index       uint64
		err         error
	)
	if dbErr := b.db.View(ctx, func(tx kv.Tx) error {
		t, blockHash, blockNumber, index, err = rawdb.ReadTransactionByHash(tx, txHash)
		return err
	}); dbErr != nil {
		return nil, types.Hash{}, 0, 0, dbErr
	}
	return t, blockHash, blockNumber, index, err
}

// ChainDb returns the underlying key-value database.
func (b *API) ChainDb() kv.RwDB {
	return b.db
}

// AccountManager returns the account manager.
func (b *API) AccountManager() *accounts.Manager {
	return b.accountManager
}

// CurrentHeader returns the current block header (alias for CurrentBlock).
func (b *API) CurrentHeader() *block.Header {
	return b.CurrentBlock()
}

// StateAtTransaction returns the execution environment of a certain transaction
// within a block. It replays all prior transactions to reconstruct the state at
// the specified transaction index.
func (eth *API) StateAtTransaction(ctx context.Context, dbTx kv.Tx, blk *block.Block, txIndex int) (*transaction.Message, evmtypes.BlockContext, *state.IntraBlockState, error) {
	if blk == nil {
		return nil, evmtypes.BlockContext{}, nil, errors.New("block is nil")
	}
	blockNumber, err := requireUint256(blk.Number64(), "block number unavailable")
	if err != nil {
		return nil, evmtypes.BlockContext{}, nil, err
	}
	if blockNumber.Uint64() == 0 {
		return nil, evmtypes.BlockContext{}, nil, errors.New("no transaction in genesis")
	}

	iParent := eth.BlockChain().GetBlock(blk.ParentHash(), blockNumber.Uint64()-1)
	if iParent == nil {
		return nil, evmtypes.BlockContext{}, nil, fmt.Errorf("parent %#x not found", blk.ParentHash())
	}
	parent, ok := iParent.(*block.Block)
	if !ok {
		return nil, evmtypes.BlockContext{}, nil, fmt.Errorf("unexpected parent block type for %#x", blk.ParentHash())
	}

	statedb, err := eth.StateAtBlock(ctx, dbTx, parent)
	if err != nil {
		return nil, evmtypes.BlockContext{}, nil, err
	}
	if txIndex == 0 && len(blk.Transactions()) == 0 {
		return nil, evmtypes.BlockContext{}, statedb, nil
	}

	signer := transaction.MakeSignerWithTimestamp(eth.BlockChain().Config(), uint256ToBigOrZero(blk.Number64()), blk.Time())
	getHeader := func(hash types.Hash, number uint64) *block.Header {
		return rawdb.ReadHeader(dbTx, hash, number)
	}
	blkHeader, ok := blk.Header().(*block.Header)
	if !ok {
		return nil, evmtypes.BlockContext{}, nil, errors.New("unexpected block header type")
	}
	blockContext := internal.NewEVMBlockContext(blkHeader, internal.GetHashFn(blkHeader, getHeader), eth.Engine(), nil)
	vmenv := vm.NewEVM(blockContext, evmtypes.TxContext{}, statedb, eth.BlockChain().Config(), vm.Config{})
	rules := vmenv.ChainRules()

	for idx, tx := range blk.Transactions() {
		statedb.Prepare(tx.Hash(), blk.Hash(), idx)

		msg, _ := tx.AsMessage(signer, blk.BaseFee64())
		if msg.FeeCap().IsZero() && eth.Engine() != nil {
			syscall := func(contract types.Address, data []byte) ([]byte, error) {
				return internal.SysCallContract(contract, data, *eth.BlockChain().Config(), statedb, blkHeader, eth.Engine())
			}
			msg.SetIsFree(eth.Engine().IsServiceTransaction(msg.From(), syscall))
		}

		txContext := internal.NewEVMTxContext(msg)
		if idx == txIndex {
			return &msg, blockContext, statedb, nil
		}
		vmenv.Reset(txContext, statedb)

		if _, err := internal.ApplyMessage(vmenv, msg, new(gaspool.GasPool).AddGas(tx.Gas()), true /* refunds */, false /* gasBailout */); err != nil {
			return nil, evmtypes.BlockContext{}, nil, fmt.Errorf("transaction %x failed: %w", tx.Hash(), err)
		}
		stateReader, err := requirePlainStateReader(statedb, "unexpected state reader type")
		if err != nil {
			return nil, evmtypes.BlockContext{}, nil, err
		}
		_ = statedb.FinalizeTx(rules, stateReader)

		if idx+1 == len(blk.Transactions()) {
			return nil, blockContext, statedb, nil
		}
	}
	return nil, evmtypes.BlockContext{}, nil, fmt.Errorf("transaction index %d out of range for block %#x", txIndex, blk.Hash())
}

// StateAtBlock retrieves the state database associated with a certain block.
func (eth *API) StateAtBlock(ctx context.Context, tx kv.Tx, blk *block.Block) (*state.IntraBlockState, error) {
	if blk == nil {
		return nil, errors.New("block is nil")
	}
	blockNumber, err := requireUint256(blk.Number64(), "block number unavailable")
	if err != nil {
		return nil, err
	}
	origin := blockNumber.Uint64()
	stateIface := eth.BlockChain().StateAt(tx, origin)
	statedb, err := requireIntraBlockState(stateIface, "failed to retrieve state for block")
	if err != nil {
		return nil, err
	}
	return statedb, nil
}
