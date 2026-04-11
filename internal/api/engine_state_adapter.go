// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

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

	// Execute against the parent snapshot, i.e. the state at the start of this block.
	// This matches the stateful Engine payload builder path and preserves historical
	// account metadata such as code hashes reconstructed from change history.
	reader := state.NewPlainState(tx, blockNum)
	writer := state.NewPlainStateWriter(tx, tx, blockNum)
	ibs := state.New(reader)
	ethel.SetupStateRootComputer(tx, ibs)
	if err := ethel.InitHashState(tx); err != nil {
		return false, types.Hash{}, err
	}

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
	result, err := ethel.ProcessBlock(a.chainCfg, a.engine, header, blk.Transactions(), uncles, ibs, blockHashFunc, nil)
	if err != nil {
		return false, types.Hash{}, fmt.Errorf("execute block %d: %w", blockNum, err)
	}

	// Verify gas.
	if result.GasUsed != header.GasUsed {
		return false, types.Hash{}, fmt.Errorf("gas mismatch: got %d, want %d", result.GasUsed, header.GasUsed)
	}

	// Commit block state into the transaction, then verify the canonical
	// Ethereum MPT root before exposing any changes to the database.
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
	computedRoot, err := ethel.VerifyStateRoot(tx)
	if err != nil {
		return false, types.Hash{}, fmt.Errorf("verify state root: %w", err)
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
	return ethel.Reorg(a.db, a.freezer, targetBlock)
}

// CurrentHead returns the current chain tip block as recorded in chaindata.
// It is used by the EngineAPIV1 nil-bc fallback so that
// engine_forkchoiceUpdated calls report VALID (not SYNCING) once the
// EthEL adapter has executed up to the head. Returns nil when chaindata
// has no head pointer yet.
func (a *EngineStateAdapter) CurrentHead() *block.Block {
	tx, err := a.db.BeginRo(context.Background())
	if err != nil {
		return nil
	}
	defer tx.Rollback()
	headHash := rawdb.ReadHeadBlockHash(tx)
	if headHash == (types.Hash{}) {
		return nil
	}
	blk, err := rawdb.ReadBlockByHash(tx, headHash)
	if err != nil || blk == nil {
		return nil
	}
	return blk
}

// CurrentHeadHash returns the current head block hash without materialising
// the body. Cheaper than CurrentHead when the caller only needs the hash.
func (a *EngineStateAdapter) CurrentHeadHash() types.Hash {
	tx, err := a.db.BeginRo(context.Background())
	if err != nil {
		return types.Hash{}
	}
	defer tx.Rollback()
	return rawdb.ReadHeadBlockHash(tx)
}

// HeaderByHash returns the EL block header for the given hash, or nil
// if chaindata does not have it. Used by the EngineAPIV1 nil-bc
// fallback path for parentHeader lookups: without it, the EL side of
// NewPayload cannot resolve the parent when the CL asks whether a
// received payload extends the current head.
func (a *EngineStateAdapter) HeaderByHash(hash types.Hash) *block.Header {
	tx, err := a.db.BeginRo(context.Background())
	if err != nil {
		return nil
	}
	defer tx.Rollback()
	hdr, err := rawdb.ReadHeaderByHash(tx, hash)
	if err != nil || hdr == nil {
		return nil
	}
	return hdr
}
