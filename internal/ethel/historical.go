// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// historical.go — historical state provider for JSON-RPC queries.
//
// HistoricalState serves eth_getBalance, eth_getCode, eth_getStorageAt
// and eth_call at arbitrary past block numbers by reading the current
// PlainState and replaying account and storage changesets backwards from
// the tip. A dedicated BlockContext per query isolates the historical
// view so that concurrent JSON-RPC requests do not interfere with the
// executor's write path.

package ethel

import (
	"context"
	"fmt"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"

	iinternal "github.com/n42blockchain/N42/internal"
)

// HistoricalState provides historical state queries for eth_call, eth_getBalance, etc.
// It reads from the current state and replays changesets backwards to reconstruct
// the state at any past block.
type HistoricalState struct {
	db       kv.RwDB
	chainCfg *params.ChainConfig
	engine   consensus.Engine
}

// NewHistoricalState creates a historical state provider.
func NewHistoricalState(db kv.RwDB, cfg *params.ChainConfig, engine consensus.Engine) *HistoricalState {
	return &HistoricalState{db: db, chainCfg: cfg, engine: engine}
}

// GetBalance returns the balance of an address at a specific block number.
func (h *HistoricalState) GetBalance(addr types.Address, blockNum uint64) (*uint256.Int, error) {
	tx, err := h.db.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	reader, err := state.NewStateHistoryReader(tx, tx, blockNum)
	if err != nil {
		return nil, fmt.Errorf("create history reader at %d: %w", blockNum, err)
	}
	acc, err := reader.ReadAccountData(addr)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return uint256.NewInt(0), nil
	}
	return &acc.Balance, nil
}

// GetStorageAt returns a storage value at a specific block number.
func (h *HistoricalState) GetStorageAt(addr types.Address, slot types.Hash, blockNum uint64) ([]byte, error) {
	tx, err := h.db.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	reader, err := state.NewStateHistoryReader(tx, tx, blockNum)
	if err != nil {
		return nil, err
	}
	acc, err := reader.ReadAccountData(addr)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return nil, nil
	}
	return reader.ReadAccountStorage(addr, &slot)
}

// GetAccount returns full account data at a specific block number.
func (h *HistoricalState) GetAccount(addr types.Address, blockNum uint64) (*account.StateAccount, error) {
	tx, err := h.db.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	reader, err := state.NewStateHistoryReader(tx, tx, blockNum)
	if err != nil {
		return nil, err
	}
	return reader.ReadAccountData(addr)
}

// Call executes an eth_call at a specific block number.
func (h *HistoricalState) Call(
	from types.Address,
	to *types.Address,
	gas uint64,
	value *uint256.Int,
	data []byte,
	blockNum uint64,
) ([]byte, uint64, error) {
	tx, err := h.db.BeginRo(context.Background())
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()

	// Read block header for EVM context.
	hash, err := rawdb.ReadCanonicalHash(tx, blockNum)
	if err != nil {
		return nil, 0, fmt.Errorf("read canonical hash: %w", err)
	}
	header := rawdb.ReadHeader(tx, hash, blockNum)
	if header == nil {
		return nil, 0, fmt.Errorf("header not found for block %d", blockNum)
	}

	// Create historical state reader.
	reader, err := state.NewStateHistoryReader(tx, tx, blockNum)
	if err != nil {
		return nil, 0, err
	}
	ibs := state.New(reader)

	// Create EVM.
	blockHashFunc := func(n uint64) types.Hash {
		h, _ := rawdb.ReadCanonicalHash(tx, n)
		return h
	}
	blockContext := iinternal.NewEVMBlockContext(header, blockHashFunc, h.engine, h.chainCfg, nil)
	txContext := evmtypes.TxContext{
		Origin:   from,
		GasPrice: uint256.NewInt(0),
	}
	evm := vm2.NewEVM(blockContext, txContext, ibs, h.chainCfg, vm2.Config{NoBaseFee: true})

	// Execute.
	if gas == 0 {
		gas = header.GasLimit
	}
	if value == nil {
		value = uint256.NewInt(0)
	}
	var toAddr types.Address
	if to != nil {
		toAddr = *to
	}
	result, leftOver, err := evm.Call(vm2.AccountRef(from), toAddr, data, gas, value, false)
	return result, gas - leftOver, err
}
