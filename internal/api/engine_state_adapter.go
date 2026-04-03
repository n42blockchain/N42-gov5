// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// engine_state_adapter.go bridges the Engine API with the ETH EL state
// management layer. When running in ETH EL profile, NewPayload executes
// blocks with persistent state writes, and ForkchoiceUpdated manages
// the canonical chain head.

package api

import (
	"context"
	"fmt"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	internalcore "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/ethel"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// EngineStateAdapter bridges Engine API with persistent state execution
// for the ETH EL profile. It executes payloads, persists state to MDBX,
// appends receipts/changesets to the Freezer, and verifies state roots.
type EngineStateAdapter struct {
	db       kv.RwDB
	freezer  *freezer.Freezer
	chainCfg *params.ChainConfig
	engine   consensus.Engine
}

// NewEngineStateAdapter creates a new adapter for ETH EL Engine API.
func NewEngineStateAdapter(db kv.RwDB, f *freezer.Freezer, cfg *params.ChainConfig, engine consensus.Engine) *EngineStateAdapter {
	return &EngineStateAdapter{
		db:       db,
		freezer:  f,
		chainCfg: cfg,
		engine:   engine,
	}
}

// ExecutePayload executes a block from the CL via Engine API with persistent
// state writes. Returns (valid, stateRoot, error).
func (a *EngineStateAdapter) ExecutePayload(blk *block.Block) (bool, types.Hash, error) {
	header := blk.Header().(*block.Header)
	blockNum := header.Number.Uint64()

	tx, err := a.db.BeginRw(context.Background())
	if err != nil {
		return false, types.Hash{}, err
	}
	defer tx.Rollback()

	// Set up state.
	reader := state.NewPlainStateReader(tx)
	writer := state.NewPlainStateWriter(tx, tx, blockNum)
	ibs := state.New(reader)

	// Attach TrieRootComputer for state root verification.
	ethel.SetupStateRootComputerPublic(tx, ibs)

	// Execute block.
	gasUsed, err := a.processBlock(tx, blk, header, ibs, writer)
	if err != nil {
		return false, types.Hash{}, fmt.Errorf("execute block %d: %w", blockNum, err)
	}

	// Verify gas.
	if gasUsed != header.GasUsed {
		return false, types.Hash{}, fmt.Errorf("gas mismatch: got %d, want %d", gasUsed, header.GasUsed)
	}

	// Commit state.
	rules := a.chainCfg.Rules(blockNum)
	if err := ibs.CommitBlock(rules, writer); err != nil {
		return false, types.Hash{}, err
	}
	if err := writer.WriteChangeSets(); err != nil {
		return false, types.Hash{}, err
	}
	if err := writer.WriteHistory(); err != nil {
		return false, types.Hash{}, err
	}

	// Verify state root via full re-hash.
	computedRoot, err := ethel.FullStateRootVerify(tx)
	if err != nil {
		return false, types.Hash{}, fmt.Errorf("state root computation: %w", err)
	}
	if computedRoot != header.Root {
		log.Error("State root mismatch",
			"block", blockNum,
			"computed", computedRoot.Hex(),
			"expected", header.Root.Hex())
		return false, computedRoot, nil
	}

	if err := tx.Commit(); err != nil {
		return false, types.Hash{}, err
	}

	log.Info("Payload executed and verified",
		"block", blockNum,
		"root", header.Root.Hex(),
		"txs", len(blk.Transactions()),
		"gas", gasUsed)

	return true, computedRoot, nil
}

// processBlock runs EVM execution for a single block.
func (a *EngineStateAdapter) processBlock(tx kv.RwTx, blk *block.Block, header *block.Header, ibs *state.IntraBlockState, writer state.WriterWithChangeSets) (uint64, error) {
	usedGas := new(uint64)
	gp := new(common.GasPool)
	gp.AddGas(header.GasLimit)

	cfg := vm2.Config{}

	// DAO fork.
	if a.chainCfg.DAOForkSupport && a.chainCfg.DAOForkBlock != nil &&
		a.chainCfg.DAOForkBlock.Cmp(header.Number.ToBig()) == 0 {
		misc.ApplyDAOHardFork(ibs)
	}

	// Pre-block system calls.
	if err := internalcore.ProcessExecutionBlockStart(nil, a.chainCfg, ibs, header, a.engine); err != nil {
		return 0, err
	}

	noop := state.NewNoopWriter()
	blockHash := header.Hash()

	for i, txn := range blk.Transactions() {
		ibs.Prepare(txn.Hash(), blockHash, i)
		_, _, err := internalcore.ApplyTransaction(
			a.chainCfg, func(n uint64) types.Hash { return types.Hash{} },
			a.engine, nil, gp, ibs, noop, header, txn, usedGas, cfg,
		)
		if err != nil {
			return 0, fmt.Errorf("tx %d: %w", i, err)
		}
	}

	// Post-block system calls.
	if _, err := internalcore.ProcessExecutionBlockEnd(nil, a.chainCfg, ibs, header, a.engine); err != nil {
		return 0, err
	}

	// Finalize (block rewards — post-merge returns nil).
	if _, _, err := a.engine.Finalize(nil, header, ibs, blk.Transactions(), nil); err != nil {
		return 0, err
	}

	return *usedGas, nil
}
