// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// EngineStateAdapter bridges the Engine API with persistent state execution.
// Wraps a kv.RwDB, freezer, chain configuration and consensus engine so
// that ExecutePayload can run a CL-supplied block through the EVM,
// commit the resulting state and verify the declared state root. Provides
// the thin glue layer between newPayload/forkchoiceUpdated RPC handlers
// and the internal ethel execution pipeline.

package api

import (
	"bytes"
	"context"
	"fmt"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	internalcore "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/cs"
	"github.com/n42blockchain/N42/internal/ethel"
	vm2 "github.com/n42blockchain/N42/internal/vm"
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

	// csSource overrides Reorg's data source. nil falls back to a.freezer.
	csSource cs.Source
}

type enginePayloadExecutionResult struct {
	stateRoot       types.Hash
	validationError error
}

// NewEngineStateAdapter creates a new adapter.
func NewEngineStateAdapter(db kv.RwDB, f *freezer.Freezer, cfg *params.ChainConfig, engine consensus.Engine) *EngineStateAdapter {
	return &EngineStateAdapter{db: db, freezer: f, chainCfg: cfg, engine: engine}
}

// WithCSSource overrides Reorg's data source (e.g. cs.TieredSource
// over warm + freezer). Pass nil to revert to direct freezer reads.
func (a *EngineStateAdapter) WithCSSource(src cs.Source) *EngineStateAdapter {
	a.csSource = src
	return a
}

// ExecutePayload executes a block from the CL, persists state, verifies root.
// Returns (valid bool, stateRoot, error).
func (a *EngineStateAdapter) ExecutePayload(blk *block.Block) (bool, types.Hash, error) {
	result, err := a.executePayloadDetailed(blk, nil, nil, nil)
	if err != nil {
		return false, types.Hash{}, err
	}
	if result.validationError != nil {
		return false, result.stateRoot, nil
	}
	return true, result.stateRoot, nil
}

// ExecutePayloadFromWire executes a block decoded directly from the
// eth/68-69 devp2p wire form: header + raw transactions + withdrawals.
// Unlike the CL path, the Pectra+ execution requests hash is anchored
// to header.RequestsHash carried on the wire (no separate CL-supplied
// requests list), and withdrawals are sourced from the body rather than
// engine_newPayloadV4 args. Returns (valid, computed state root, err).
func (a *EngineStateAdapter) ExecutePayloadFromWire(blk *block.Block, withdrawals []*Withdrawal) (bool, types.Hash, error) {
	var parentBeaconRoot *types.Hash
	if hdr, _ := blk.Header().(*block.Header); hdr != nil && hdr.ParentBeaconRoot != nil {
		v := *hdr.ParentBeaconRoot
		parentBeaconRoot = &v
	}
	result, err := a.executePayloadDetailed(blk, parentBeaconRoot, nil, withdrawals)
	if err != nil {
		return false, types.Hash{}, err
	}
	if result.validationError != nil {
		return false, result.stateRoot, nil
	}
	return true, result.stateRoot, nil
}

func (a *EngineStateAdapter) executePayloadDetailed(blk *block.Block, parentBeaconRoot *types.Hash, expectedRequests []hexutil.Bytes, withdrawals []*Withdrawal) (*enginePayloadExecutionResult, error) {
	header := blk.Header().(*block.Header)
	if header == nil || header.Number == nil {
		return nil, fmt.Errorf("payload header missing block number")
	}
	if parentBeaconRoot == nil {
		parentBeaconRoot = header.ParentBeaconRoot
	}
	blockNum := header.Number.Uint64()

	tx, err := a.db.BeginRw(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Execute against the parent snapshot, i.e. the state at the start of this block.
	// This matches the stateful Engine payload builder path and preserves historical
	// account metadata such as code hashes reconstructed from change history.
	reader := state.NewPlainState(tx, blockNum)
	writer := state.NewPlainStateWriter(tx, tx, blockNum)
	ibs := state.New(reader)
	ibs.BeginWriteCodes()
	ethel.SetupStateRootComputer(tx, ibs)
	if err := ethel.InitHashState(tx); err != nil {
		return nil, err
	}

	expected := captureExpectedExecutionPayloadOutputs(header)
	getHeader := func(_ types.Hash, number uint64) *block.Header {
		canonicalHash, err := rawdb.ReadCanonicalHash(tx, number)
		if err != nil || canonicalHash == (types.Hash{}) {
			return nil
		}
		return rawdb.ReadHeader(tx, canonicalHash, number)
	}
	blockHashFunc := internalcore.GetHashFn(header, getHeader)

	gasPool := new(common.GasPool)
	gasPool.AddGas(blk.GasLimit())
	var usedGas uint64
	receipts := make(block.Receipts, 0, len(blk.Transactions()))
	if err := internalcore.ProcessExecutionBlockStart(parentBeaconRoot, a.chainCfg, ibs, header, a.engine); err != nil {
		return nil, err
	}
	for i, txn := range blk.Transactions() {
		ibs.Prepare(txn.Hash(), blk.Hash(), i)
		receipt, _, err := internalcore.ApplyTransaction(a.chainCfg, blockHashFunc, a.engine, nil, gasPool, ibs, state.NewNoopWriter(), header, txn, &usedGas, vm2.Config{})
		if err != nil {
			return nil, err
		}
		if receipt != nil {
			receipts = append(receipts, receipt)
		}
	}
	if usedGas != header.GasUsed {
		return &enginePayloadExecutionResult{
			validationError: fmt.Errorf("gas mismatch: got %d, want %d", usedGas, header.GasUsed),
		}, nil
	}
	if err := finalizeExecutionStateChanges(a.chainCfg, header, ibs); err != nil {
		return nil, err
	}
	applyExecutionWithdrawals(ibs, withdrawals)
	var actualRequests []hexutil.Bytes
	if actualRequests, err = internalcore.ProcessExecutionBlockEnd(receipts, a.chainCfg, ibs, header, a.engine); err != nil {
		return nil, err
	}
	rules := a.chainCfg.RulesWithTimestamp(blockNum, header.Time)
	if rules != nil && rules.IsPrague {
		actualHash := executionRequestsHash(actualRequests)
		// Pick the trust anchor for the requests hash. Three cases:
		//   1. CL-driven (NewPayloadV4): expectedRequests is the
		//      authoritative list from the beacon block — compare
		//      against its hash.
		//   2. Wire-driven (devp2p sync): the caller didn't pass a
		//      list, but the wire header carries header.RequestsHash
		//      — that's the anchor.
		//   3. Neither: pre-existing tests with toy blocks that have
		//      no requests anywhere. Hash empty list and compare.
		var expectedHash types.Hash
		switch {
		case expectedRequests != nil:
			expectedHash = executionRequestsHash(expectedRequests)
		case header.RequestsHash != nil:
			expectedHash = *header.RequestsHash
		default:
			expectedHash = executionRequestsHash(nil)
		}
		if actualHash != expectedHash {
			return &enginePayloadExecutionResult{
				validationError: fmt.Errorf("invalid requests hash"),
			}, nil
		}
		header.RequestsHash = &actualHash
	}
	actualReceiptHash := ethel.EthReceiptHash(receipts)
	actualBloom := block.CreateBloom(receipts)
	if expected != nil && actualReceiptHash != expected.receiptsRoot {
		return &enginePayloadExecutionResult{
			validationError: fmt.Errorf("receipts root mismatch"),
		}, nil
	}
	if expected != nil && !bytes.Equal(actualBloom.Bytes(), expected.logsBloom) {
		return &enginePayloadExecutionResult{
			validationError: fmt.Errorf("logs bloom mismatch"),
		}, nil
	}
	header.ReceiptHash = actualReceiptHash
	header.Bloom = actualBloom
	header.Root = ibs.IntermediateRoot()
	blk.WithSeal(header)

	// Commit block state into the transaction, then verify the canonical
	// Ethereum MPT root before exposing any changes to the database.
	if err := ibs.CommitBlock(rules, writer); err != nil {
		return nil, err
	}
	if err := writer.WriteChangeSets(); err != nil {
		return nil, err
	}
	if err := writer.WriteHistory(); err != nil {
		return nil, err
	}
	computedRoot, err := ethel.VerifyStateRoot(tx)
	if err != nil {
		return nil, fmt.Errorf("verify state root: %w", err)
	}
	if expected != nil && computedRoot != expected.stateRoot {
		log.Error("State root mismatch", "block", blockNum,
			"computed", computedRoot.Hex(), "expected", expected.stateRoot.Hex())
		return &enginePayloadExecutionResult{
			stateRoot:       computedRoot,
			validationError: fmt.Errorf("state root mismatch"),
		}, nil
	}
	header.Root = computedRoot
	blk.WithSeal(header)

	// Persist the executed payload so subsequent head/header lookups can
	// resolve Engine eth-compatible hashes back to the underlying block.
	storedHash, err := writeEnginePayloadBlock(tx, blk)
	if err != nil {
		return nil, err
	}
	if err := rawdb.WriteCanonicalHash(tx, storedHash, blockNum); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	log.Info("Payload executed", "block", blockNum, "root", header.Root.Hex(),
		"txs", len(blk.Transactions()), "gas", usedGas)
	return &enginePayloadExecutionResult{stateRoot: computedRoot}, nil
}

// ForkchoiceUpdated updates the canonical chain head.
func (a *EngineStateAdapter) ForkchoiceUpdated(headHash, safeHash, finalizedHash types.Hash) error {
	tx, err := a.db.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback()

	storedHeadHash, err := a.resolveCanonicalStoredHash(tx, headHash)
	if err != nil {
		return err
	}
	if storedHeadHash == (types.Hash{}) {
		return fmt.Errorf("head block not found: %s", headHash.Hex())
	}
	if safeHash != (types.Hash{}) {
		if _, err := a.resolveCanonicalStoredHash(tx, safeHash); err != nil {
			return err
		}
	}
	if finalizedHash != (types.Hash{}) {
		if _, err := a.resolveCanonicalStoredHash(tx, finalizedHash); err != nil {
			return err
		}
	}

	rawdb.WriteHeadBlockHash(tx, storedHeadHash)
	if err := rawdb.WriteHeadHeaderHash(tx, storedHeadHash); err != nil {
		return err
	}
	headerNum := rawdb.ReadHeaderNumber(tx, storedHeadHash)

	if err := tx.Commit(); err != nil {
		return err
	}

	if headerNum != nil {
		log.Info("Forkchoice updated", "head", headHash.Hex()[:10], "block", *headerNum)
	}
	return nil
}

// Reorg rolls back state to the given block number using changesets.
func (a *EngineStateAdapter) Reorg(targetBlock uint64) error {
	if a.csSource != nil {
		return ethel.ReorgWithSource(a.db, a.csSource, targetBlock)
	}
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
	headHash := rawdb.ReadHeadBlockHash(tx)
	if headHash == (types.Hash{}) {
		return types.Hash{}
	}
	blk, err := rawdb.ReadBlockByHash(tx, headHash)
	if err == nil && blk != nil {
		return ethCompatibleBlockHash(blk, a.chainCfg)
	}
	hdr, err := rawdb.ReadHeaderByHash(tx, headHash)
	if err != nil || hdr == nil {
		return types.Hash{}
	}
	return ethCompatibleHeaderHash(hdr, a.chainCfg)
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
	if err == nil && hdr != nil {
		return hdr
	}
	headHash := rawdb.ReadHeadBlockHash(tx)
	if headHash == (types.Hash{}) {
		return nil
	}
	blk, err := rawdb.ReadBlockByHash(tx, headHash)
	if err != nil || blk == nil {
		return nil
	}
	if ethCompatibleBlockHash(blk, a.chainCfg) != hash {
		return nil
	}
	headHdr, ok := blk.Header().(*block.Header)
	if !ok {
		return nil
	}
	return headHdr
}

func (a *EngineStateAdapter) resolveCanonicalStoredHash(tx kv.Tx, engineHash types.Hash) (types.Hash, error) {
	if engineHash == (types.Hash{}) {
		return types.Hash{}, nil
	}
	if hdr, err := rawdb.ReadHeaderByHash(tx, engineHash); err == nil && hdr != nil {
		return engineHash, nil
	}
	for number := uint64(0); ; number++ {
		storedHash, err := rawdb.ReadCanonicalHash(tx, number)
		if err != nil {
			return types.Hash{}, err
		}
		if storedHash == (types.Hash{}) {
			return types.Hash{}, nil
		}
		blk, err := rawdb.ReadBlockByHash(tx, storedHash)
		if err != nil {
			return types.Hash{}, err
		}
		if blk != nil && ethCompatibleBlockHash(blk, a.chainCfg) == engineHash {
			return storedHash, nil
		}
		hdr, err := rawdb.ReadHeaderByHash(tx, storedHash)
		if err != nil {
			return types.Hash{}, err
		}
		if hdr != nil && ethCompatibleHeaderHash(hdr, a.chainCfg) == engineHash {
			return storedHash, nil
		}
	}
}

func writeEnginePayloadBlock(tx kv.RwTx, blk *block.Block) (types.Hash, error) {
	header, ok := blk.Header().(*block.Header)
	if !ok || header == nil {
		return types.Hash{}, fmt.Errorf("unexpected header type")
	}
	hash := header.Hash()
	rawBody := &block.RawBody{Transactions: make([][]byte, 0, len(blk.Transactions()))}
	for _, txn := range blk.Transactions() {
		encoded, err := transaction.EncodeEthereumTransaction(txn)
		if err != nil {
			return types.Hash{}, err
		}
		rawBody.Transactions = append(rawBody.Transactions, encoded)
	}
	if _, _, err := rawdb.WriteRawBody(tx, hash, header.Number.Uint64(), rawBody); err != nil {
		return types.Hash{}, err
	}
	rawdb.WriteHeader(tx, header)
	return hash, nil
}
