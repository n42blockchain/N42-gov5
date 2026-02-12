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
	common2 "github.com/n42blockchain/N42/common"
	types "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	common "github.com/n42blockchain/N42/common/types"
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

func (b *API) CurrentBlock() *types.Header {
	blk := b.bc.CurrentBlock()
	if blk == nil {
		return nil
	}
	h := blk.Header()
	if h == nil {
		return nil
	}
	header, ok := h.(*types.Header)
	if !ok {
		return nil
	}
	return header
}

func (b *API) HeaderByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Header, error) {
	// Otherwise resolve and return the block
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
	header, ok := iHeader.(*types.Header)
	if !ok {
		return nil, errors.New("unexpected header type")
	}
	return header, nil
}

func (b *API) HeaderByNumberOrHash(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (*types.Header, error) {
	if blockNr, ok := blockNrOrHash.Number(); ok {
		return b.HeaderByNumber(ctx, blockNr)
	}
	if hash, ok := blockNrOrHash.Hash(); ok {
		header, _ := b.bc.GetHeaderByHash(hash)
		if header == nil {
			return nil, errors.New("header for hash not found")
		}
		if blockNrOrHash.RequireCanonical && b.bc.(*internal.BlockChain).GetCanonicalHash(header.Number64()) != hash {
			return nil, errors.New("hash is not currently canonical")
		}
		h, ok := header.(*types.Header)
		if !ok {
			return nil, errors.New("unexpected header type")
		}
		return h, nil
	}
	return nil, errors.New("invalid arguments; neither block nor hash specified")
}

func (b *API) HeaderByHash(ctx context.Context, hash common.Hash) (*types.Header, error) {
	iHeader, _ := b.bc.GetHeaderByHash(hash)
	if iHeader == nil {
		return nil, nil
	}
	h, ok := iHeader.(*types.Header)
	if !ok {
		return nil, errors.New("unexpected header type")
	}
	return h, nil
}

func (b *API) BlockByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Block, error) {
	// Otherwise resolve and return the block
	if number == rpc.LatestBlockNumber {
		header := b.bc.CurrentBlock()
		if header == nil {
			return nil, errors.New("current block not available")
		}
		iBlock := b.bc.GetBlock(header.Hash(), header.Number64().Uint64())
		if iBlock == nil {
			return nil, errors.New("current block body not found")
		}
		blk, ok := iBlock.(*types.Block)
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
	blk, ok := iBlock.(*types.Block)
	if !ok {
		return nil, errors.New("unexpected block type")
	}
	return blk, nil
}

func (b *API) BlockByHash(ctx context.Context, hash common.Hash) (*types.Block, error) {
	iBlock, _ := b.bc.GetBlockByHash(hash)
	if iBlock == nil {
		return nil, nil
	}
	blk, ok := iBlock.(*types.Block)
	if !ok {
		return nil, errors.New("unexpected block type")
	}
	return blk, nil
}

func (b *API) BlockByNumberOrHash(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (*types.Block, error) {
	if blockNr, ok := blockNrOrHash.Number(); ok {
		return b.BlockByNumber(ctx, blockNr)
	}
	if hash, ok := blockNrOrHash.Hash(); ok {
		header, _ := b.bc.GetHeaderByHash(hash)
		if header == nil {
			return nil, errors.New("header for hash not found")
		}
		if blockNrOrHash.RequireCanonical && b.bc.(*internal.BlockChain).GetCanonicalHash(header.Number64()) != hash {
			return nil, errors.New("hash is not currently canonical")
		}
		iBlock := b.bc.GetBlock(hash, header.Number64().Uint64())
		if iBlock == nil {
			return nil, errors.New("header found, but block body is missing")
		}
		blk, ok := iBlock.(*types.Block)
		if !ok {
			return nil, errors.New("unexpected block type")
		}
		return blk, nil
	}
	return nil, errors.New("invalid arguments; neither block nor hash specified")
}

func (b *API) GetReceipts(ctx context.Context, hash common.Hash) (types.Receipts, error) {
	return b.bc.GetReceipts(hash)
}

func (b *API) GetTd(ctx context.Context, hash common.Hash) *uint256.Int {
	if header, _ := b.bc.GetHeaderByHash(hash); header != nil {
		return b.bc.GetTd(hash, header.Number64())
	}
	return nil
}

func (b *API) GetEVM(ctx context.Context, msg *transaction.Message, state *state.IntraBlockState, header *types.Header, vmConfig *vm.Config) (*vm.EVM, func() error, error) {
	if vmConfig == nil {
		//vmConfig = b.bc.GetVMConfig()
		vmConfig = &vm.Config{}
	}
	txContext := internal.NewEVMTxContext(msg)
	getHeader := func(hash common.Hash, n uint64) *types.Header {
		h := b.bc.GetHeader(hash, uint256.NewInt(n))
		if h == nil {
			return nil
		}
		th, ok := h.(*types.Header)
		if !ok {
			return nil
		}
		return th
	}
	// Type assert Engine from interface{} to consensus.Engine
	engine, ok := b.bc.Engine().(consensus.Engine)
	if !ok {
		return nil, nil, errors.New("consensus engine not available")
	}
	context := internal.NewEVMBlockContext(header, internal.GetHashFn(header, getHeader), engine, nil)
	return vm.NewEVM(context, txContext, state, b.bc.Config(), *vmConfig), state.Error, nil
}

func (b *API) GetTransaction(ctx context.Context, txHash common.Hash) (*transaction.Transaction, common.Hash, uint64, uint64, error) {
	var t *transaction.Transaction
	var blockHash common.Hash
	var index uint64
	var err error
	var blockNumber uint64
	if dbErr := b.db.View(ctx, func(tx kv.Tx) error {
		t, blockHash, blockNumber, index, err = rawdb.ReadTransactionByHash(tx, txHash)
		return err
	}); dbErr != nil {
		return nil, common.Hash{}, 0, 0, dbErr
	}
	return t, blockHash, blockNumber, index, err
}

func (b *API) ChainDb() kv.RwDB {
	return b.db
}

func (b *API) AccountManager() *accounts.Manager {
	return b.accountManager
}

func (b *API) CurrentHeader() *types.Header {
	return b.CurrentBlock()
}

// stateAtTransaction returns the execution environment of a certain transaction.
func (eth *API) StateAtTransaction(ctx context.Context, dbTx kv.Tx, blk *types.Block, txIndex int) (*transaction.Message, evmtypes.BlockContext, *state.IntraBlockState, error) {
	// Short circuit if it's genesis block.
	if blk.Number64().Uint64() == 0 {
		return nil, evmtypes.BlockContext{}, nil, errors.New("no transaction in genesis")
	}
	// Create the parent state database
	iParent := eth.BlockChain().GetBlock(blk.ParentHash(), blk.Number64().Uint64()-1)
	if iParent == nil {
		return nil, evmtypes.BlockContext{}, nil, fmt.Errorf("parent %#x not found", blk.ParentHash())
	}
	parent, ok := iParent.(*types.Block)
	if !ok {
		return nil, evmtypes.BlockContext{}, nil, fmt.Errorf("unexpected parent block type for %#x", blk.ParentHash())
	}
	// Lookup the statedb of parent block from the live database,
	// otherwise regenerate it on the flight.

	statedb, err := eth.StateAtBlock(ctx, dbTx, parent)
	if err != nil {
		return nil, evmtypes.BlockContext{}, nil, err
	}
	if txIndex == 0 && len(blk.Transactions()) == 0 {
		return nil, evmtypes.BlockContext{}, statedb, nil
	}
	// Recompute transactions up to the target index.
	signer := transaction.MakeSigner(eth.BlockChain().Config(), blk.Number64().ToBig())
	getHeader := func(hash common.Hash, number uint64) *types.Header {
		return rawdb.ReadHeader(dbTx, hash, number)
	}
	blkHeader, ok := blk.Header().(*types.Header)
	if !ok {
		return nil, evmtypes.BlockContext{}, nil, errors.New("unexpected block header type")
	}
	blockContext := internal.NewEVMBlockContext(blkHeader, internal.GetHashFn(blkHeader, getHeader), eth.Engine(), nil)
	vmenv := vm.NewEVM(blockContext, evmtypes.TxContext{}, statedb, eth.BlockChain().Config(), vm.Config{})
	rules := vmenv.ChainRules()

	for idx, tx := range blk.Transactions() {
		statedb.Prepare(tx.Hash(), blk.Hash(), idx)

		// Assemble the transaction call message and return if the requested offset
		msg, _ := tx.AsMessage(signer, blk.BaseFee64())
		if msg.FeeCap().IsZero() && eth.Engine() != nil {
			syscall := func(contract common.Address, data []byte) ([]byte, error) {
				return internal.SysCallContract(contract, data, *eth.BlockChain().Config(), statedb, blkHeader, eth.Engine() /* constCall */)
			}
			msg.SetIsFree(eth.Engine().IsServiceTransaction(msg.From(), syscall))
		}

		TxContext := internal.NewEVMTxContext(msg)
		if idx == txIndex {
			return &msg, blockContext, statedb, nil
		}
		vmenv.Reset(TxContext, statedb)
		// Not yet the searched for transaction, execute on top of the current state
		if _, err := internal.ApplyMessage(vmenv, msg, new(common2.GasPool).AddGas(tx.Gas()), true /* refunds */, false /* gasBailout */); err != nil {
			return nil, evmtypes.BlockContext{}, nil, fmt.Errorf("transaction %x failed: %w", tx.Hash(), err)
		}
		// Ensure any modifications are committed to the state
		// Only delete empty objects if EIP161 (part of Spurious Dragon) is in effect
		_ = statedb.FinalizeTx(rules, statedb.GetStateReader().(*state.PlainState))

		if idx+1 == len(blk.Transactions()) {
			// Return the state from evaluating all txs in the block, note no msg or TxContext in this case
			return nil, blockContext, statedb, nil
		}
	}
	return nil, evmtypes.BlockContext{}, nil, fmt.Errorf("transaction index %d out of range for block %#x", txIndex, blk.Hash())
}

// StateAtBlock retrieves the state database associated with a certain block.
func (eth *API) StateAtBlock(ctx context.Context, tx kv.Tx, blk *types.Block) (statedb *state.IntraBlockState, err error) {
	origin := blk.Number64().Uint64()
	stateIface := eth.BlockChain().StateAt(tx, origin)
	var ok bool
	statedb, ok = stateIface.(*state.IntraBlockState)
	if !ok || statedb == nil {
		return nil, errors.New("failed to retrieve state for block")
	}
	return statedb, nil
}
