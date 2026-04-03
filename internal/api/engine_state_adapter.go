// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// engine_state_adapter.go bridges the Engine API with persistent state execution
// for the ETH EL profile.

package api

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// EngineStateAdapter bridges Engine API with persistent state execution.
type EngineStateAdapter struct {
	db       kv.RwDB
	freezer  *freezer.Freezer
	chainCfg *params.ChainConfig
	engine   consensus.Engine
}

// NewEngineStateAdapter creates a new adapter.
func NewEngineStateAdapter(db kv.RwDB, f *freezer.Freezer, cfg *params.ChainConfig, engine consensus.Engine) *EngineStateAdapter {
	return &EngineStateAdapter{db: db, freezer: f, chainCfg: cfg, engine: engine}
}

// ExecutePayload executes a block from the CL, persists state, verifies root.
// Returns (valid bool, stateRoot, error).
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

	// Build BLOCKHASH function from DB.
	blockHashFunc := func(n uint64) types.Hash {
		h, err := rawdb.ReadCanonicalHash(tx, n)
		if err != nil {
			return types.Hash{}
		}
		return h
	}

	// Execute using shared ProcessBlock.
	var uncles []block.IHeader // post-merge: no uncles
	result, err := ethel.ProcessBlock(a.chainCfg, a.engine, header, blk.Transactions(), uncles, ibs, blockHashFunc)
	if err != nil {
		return false, types.Hash{}, fmt.Errorf("execute block %d: %w", blockNum, err)
	}

	// Verify gas.
	if result.GasUsed != header.GasUsed {
		return false, types.Hash{}, fmt.Errorf("gas mismatch: got %d, want %d", result.GasUsed, header.GasUsed)
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

	// Verify state root.
	computedRoot, err := ethel.FullStateRootVerify(tx)
	if err != nil {
		return false, types.Hash{}, fmt.Errorf("state root: %w", err)
	}
	if computedRoot != header.Root {
		log.Error("State root mismatch", "block", blockNum,
			"computed", computedRoot.Hex(), "expected", header.Root.Hex())
		return false, computedRoot, nil
	}

	// Write canonical chain pointers.
	hash := header.Hash()
	if err := rawdb.WriteCanonicalHash(tx, hash, blockNum); err != nil {
		return false, types.Hash{}, err
	}

	if err := tx.Commit(); err != nil {
		return false, types.Hash{}, err
	}

	log.Info("Payload executed", "block", blockNum, "root", header.Root.Hex(),
		"txs", len(blk.Transactions()), "gas", result.GasUsed)
	return true, computedRoot, nil
}

// ForkchoiceUpdated updates the canonical chain head.
func (a *EngineStateAdapter) ForkchoiceUpdated(headHash, safeHash, finalizedHash types.Hash) error {
	tx, err := a.db.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Write forkchoice state.
	var buf [96]byte
	copy(buf[0:32], headHash[:])
	copy(buf[32:64], safeHash[:])
	copy(buf[64:96], finalizedHash[:])
	if err := tx.Put(kv.LastForkchoice, []byte("lastForkchoice"), buf[:]); err != nil {
		return err
	}

	// Update head block pointer.
	// Look up block number from hash.
	headerNum := rawdb.ReadHeaderNumber(tx, headHash)
	if headerNum == nil {
		return fmt.Errorf("head block not found: %s", headHash.Hex())
	}
	var numBuf [8]byte
	binary.BigEndian.PutUint64(numBuf[:], *headerNum)
	if err := tx.Put(kv.HeadBlockKey, []byte("LastBlock"), numBuf[:]); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	log.Info("Forkchoice updated", "head", headHash.Hex()[:10], "block", *headerNum)
	return nil
}

// Reorg rolls back state to the given block number using changesets.
func (a *EngineStateAdapter) Reorg(targetBlock uint64) error {
	return ethel.Reorg(a.db, targetBlock)
}
