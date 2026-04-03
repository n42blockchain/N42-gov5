// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"fmt"
	"time"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/changeset"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// ExecutorConfig holds configuration for the block executor.
type ExecutorConfig struct {
	// CommitInterval is how many blocks between MDBX commits.
	CommitInterval uint64
	// VerifyInterval is how many blocks between state root verification.
	// 0 = never verify (fastest), 1 = every block (safest).
	VerifyInterval uint64
	// StartBlock is the first block to execute (inclusive).
	StartBlock uint64
	// EndBlock is the last block to execute (inclusive). 0 = all available.
	EndBlock uint64
	// SkipErrors if true, log gas mismatches but continue execution.
	SkipErrors bool
	// NoIndices if true, skip writing TxLookup and LogIndex (faster sync).
	NoIndices bool
	// NoOutputs if true, skip writing output freezer (receipts, senders, etc.).
	NoOutputs bool
}

// Executor reads blocks from a Geth-compatible Freezer and re-executes
// them to produce state, changesets, and receipts.
type Executor struct {
	freezer     *freezer.Freezer // input: Geth ancient (read-only)
	outFreezer  *freezer.Freezer // output: receipts, senders, changesets
	db          kv.RwDB
	chainCfg    *params.ChainConfig
	engine      consensus.Engine
	cfg         ExecutorConfig
	headerCache map[uint64]*block.Header // small LRU for BLOCKHASH
}

// NewExecutor creates a new block executor.
func NewExecutor(f *freezer.Freezer, db kv.RwDB, chainCfg *params.ChainConfig, engine consensus.Engine, cfg ExecutorConfig, outFreezer *freezer.Freezer) *Executor {
	if cfg.CommitInterval == 0 {
		cfg.CommitInterval = 10000
	}
	return &Executor{
		freezer:     f,
		outFreezer:  outFreezer,
		db:          db,
		chainCfg:    chainCfg,
		engine:      engine,
		cfg:         cfg,
		headerCache: make(map[uint64]*block.Header, 260),
	}
}

// Run executes blocks from StartBlock to EndBlock.
func (e *Executor) Run(ctx context.Context) error {
	endBlock := e.cfg.EndBlock
	if endBlock == 0 {
		endBlock = e.freezer.Frozen() - 1
	}

	tx, err := e.db.BeginRw(ctx)
	if err != nil {
		return err
	}
	// cleanup ensures the current tx is rolled back on exit.
	cleanup := func() { tx.Rollback() }
	defer func() { cleanup() }()

	// Resume from last committed block if restarting.
	startBlock := e.cfg.StartBlock
	if saved := ReadProgress(tx); saved > 0 && saved >= startBlock {
		startBlock = saved + 1
		log.Info("Resuming execution", "from", startBlock, "lastCommitted", saved)
	}

	if startBlock > endBlock {
		log.Info("Already past target", "at", startBlock-1, "target", endBlock)
		return nil
	}

	// Initialize HashedAccounts from PlainState if verify enabled and not yet populated.
	if e.cfg.VerifyInterval > 0 {
		if err := InitHashState(tx); err != nil {
			return fmt.Errorf("init hash state: %w", err)
		}
	}

	// Pad output freezer to match startBlock.
	if e.outFreezer != nil {
		if err := e.padOutputFreezer(startBlock); err != nil {
			return fmt.Errorf("pad output freezer: %w", err)
		}
	}

	startTime := time.Now()

	for blockNum := startBlock; blockNum <= endBlock; blockNum++ {
		if ctx.Err() != nil {
			log.Info("Shutting down executor", "lastBlock", blockNum-1)
			// Save progress for the last committed block before exit.
			return ctx.Err()
		}

		if err := e.executeBlock(ctx, tx, blockNum); err != nil {
			if e.cfg.SkipErrors {
				log.Warn("Block execution error (skipped)", "block", blockNum, "err", err)
				continue
			}
			return fmt.Errorf("block %d: %w", blockNum, err)
		}

		// Periodic commit.
		if blockNum > 0 && blockNum%e.cfg.CommitInterval == 0 {
			// Save progress before commit.
			if err := WriteProgress(tx, blockNum); err != nil {
				return fmt.Errorf("write progress at block %d: %w", blockNum, err)
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit at block %d: %w", blockNum, err)
			}
			elapsed := time.Since(startTime)
			blkPerSec := float64(blockNum-startBlock+1) / elapsed.Seconds()
			log.Info("EthEL progress",
				"block", blockNum,
				"blk/s", fmt.Sprintf("%.0f", blkPerSec),
				"elapsed", elapsed.Truncate(time.Second))

			tx, err = e.db.BeginRw(ctx)
			if err != nil {
				// ctx cancelled — tx is nil, cleanup is a no-op.
				cleanup = func() {}
				return err
			}
			// Update cleanup to rollback the new tx on exit.
			cleanup = func() { tx.Rollback() }
		}
	}

	// Final commit with progress.
	if err := WriteProgress(tx, endBlock); err != nil {
		return fmt.Errorf("write final progress: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	cleanup = func() {} // committed successfully, no rollback needed

	elapsed := time.Since(startTime)
	total := endBlock - startBlock + 1
	log.Info("EthEL execution complete",
		"blocks", total,
		"elapsed", elapsed.Truncate(time.Second),
		"blk/s", fmt.Sprintf("%.0f", float64(total)/elapsed.Seconds()))
	return nil
}

// executeBlock processes a single block.
func (e *Executor) executeBlock(ctx context.Context, tx kv.RwTx, blockNum uint64) error {
	t0 := time.Now()
	// 1. Read header and body from freezer.
	header, err := e.readHeader(blockNum)
	if err != nil {
		return err
	}
	body, err := e.readBody(blockNum)
	if err != nil {
		return err
	}

	// Cache header for BLOCKHASH opcode.
	e.cacheHeader(blockNum, header)

	// 2. Set up state reader/writer.
	plainReader := state.NewPlainStateReader(tx)
	var witnessReader *WitnessStateReader
	var reader state.StateReader = plainReader
	if e.outFreezer != nil && !e.cfg.NoOutputs {
		witnessReader = NewWitnessStateReader(plainReader)
		reader = witnessReader
	}
	writer := state.NewPlainStateWriter(tx, tx, blockNum)
	ibs := state.New(reader)

	shouldVerify := e.cfg.VerifyInterval > 0 && blockNum%e.cfg.VerifyInterval == 0

	// Attach HashOnlyComputer to keep HashedAccounts in sync incrementally.
	if e.cfg.VerifyInterval > 0 {
		ibs.SetRootComputer(NewHashOnlyComputer(tx))
	}

	t1 := time.Now()
	// 3. Process block (DAO fork, system contracts, EVM).
	result, err := e.processBlock(header, body, ibs)
	if err != nil {
		return err
	}

	// 4. Verify gas used.
	if result.GasUsed != header.GasUsed {
		if e.cfg.SkipErrors {
			log.Warn("Gas mismatch (skipped)",
				"block", blockNum, "got", result.GasUsed, "want", header.GasUsed,
				"diff", int64(header.GasUsed)-int64(result.GasUsed))
		} else {
			return fmt.Errorf("gas mismatch: got %d, want %d", result.GasUsed, header.GasUsed)
		}
	}

	// 5. Flush dirty accounts to HashedAccounts via HashOnlyComputer.
	// Must run every block (not just verify blocks) to keep HashedAccounts
	// incrementally in sync. Cost is O(dirty) per block, not O(all).
	if e.cfg.VerifyInterval > 0 {
		ibs.IntermediateRoot()
	}

	t2 := time.Now()
	// 6. Commit state changes to writer.
	rules := e.chainCfg.Rules(header.Number.Uint64())
	if err := ibs.CommitBlock(rules, writer); err != nil {
		return fmt.Errorf("commit block state: %w", err)
	}
	if err := writer.WriteChangeSets(); err != nil {
		return fmt.Errorf("write changesets: %w", err)
	}
	if err := writer.WriteHistory(); err != nil {
		return fmt.Errorf("write history: %w", err)
	}

	t3 := time.Now()
	// 7. Write indices (TxLookup + LogIndex) for RPC support.
	if !e.cfg.NoIndices {
		if err := WriteBlockIndices(tx, blockNum, body.Transactions); err != nil {
			return fmt.Errorf("write tx indices: %w", err)
		}
		if err := rawdb.WriteLogIndex(tx, blockNum, result.Receipts); err != nil {
			return fmt.Errorf("write log index: %w", err)
		}
	}

	t4 := time.Now()
	// 8. Write execution outputs to output freezer.
	if e.outFreezer != nil && !e.cfg.NoOutputs {
		if err := e.writeOutputs(blockNum, result, writer, witnessReader, tx); err != nil {
			return fmt.Errorf("write outputs: %w", err)
		}
	}

	// 8. Verify state root (if enabled).
	// HashedAccounts are kept in sync by HashOnlyComputer; only CalcTrieRoot needed.
	if shouldVerify {
		computedRoot, err := CalcStateRoot(tx)
		if err != nil {
			return fmt.Errorf("state root computation: %w", err)
		}
		if computedRoot != header.Root {
			return fmt.Errorf("state root mismatch at block %d: computed %s, expected %s",
				blockNum, computedRoot.Hex(), header.Root.Hex())
		}
		log.Info("State root verified", "block", blockNum, "root", header.Root.Hex())
	}

	t5 := time.Now()

	// Performance log every 1000 blocks.
	if blockNum%1000 == 0 && blockNum > 0 {
		log.Info("Block timing",
			"block", blockNum,
			"setup", t1.Sub(t0),
			"evm", t2.Sub(t1),
			"commit", t3.Sub(t2),
			"indices", t4.Sub(t3),
			"outputs", t5.Sub(t4),
			"total", t5.Sub(t0))
	}

	return nil
}

// BlockResult is defined in process.go. This comment prevents confusion.
// processBlock delegates to the shared ProcessBlock function.
func (e *Executor) processBlock(header *block.Header, body *GethBodyResult, ibs *state.IntraBlockState) (*BlockResult, error) {
	var uncles []block.IHeader
	for _, u := range body.Uncles {
		uncles = append(uncles, u)
	}
	return ProcessBlock(e.chainCfg, e.engine, header, body.Transactions, uncles, ibs, e.makeBlockHashFunc(header))
}

// readHeader reads and decodes a Geth RLP header from the freezer.
func (e *Executor) readHeader(blockNum uint64) (*block.Header, error) {
	data, err := e.freezer.Ancient(freezer.TableHeaders, blockNum)
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	return DecodeGethHeader(data)
}

// readBody reads and decodes a Geth RLP body from the freezer.
func (e *Executor) readBody(blockNum uint64) (*GethBodyResult, error) {
	data, err := e.freezer.Ancient(freezer.TableBodies, blockNum)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return DecodeGethBody(data)
}

// makeBlockHashFunc creates a BLOCKHASH function using the header cache.
func (e *Executor) makeBlockHashFunc(ref *block.Header) func(n uint64) types.Hash {
	return func(n uint64) types.Hash {
		refNum := ref.Number.Uint64()
		if n >= refNum {
			return types.Hash{}
		}
		if h, ok := e.headerCache[n]; ok {
			return h.Hash()
		}
		// Read from freezer if not cached.
		h, err := e.readHeader(n)
		if err != nil {
			return types.Hash{}
		}
		e.cacheHeader(n, h)
		return h.Hash()
	}
}

// padOutputFreezer writes empty entries for blocks 0..startBlock-1 so that
// the output freezer's item numbering aligns with block numbers.
func (e *Executor) padOutputFreezer(startBlock uint64) error {
	tables := []struct {
		name string
		ext  string
	}{
		{freezer.TableReceipts, "c"},
		{freezer.TableSenders, "c"},
		{freezer.TableAccountChanges, "c"},
		{freezer.TableStorageChanges, "c"},
		{freezer.TableLeavesJournal, "c"},
		{freezer.TableBlockWitness, "c"},
	}
	for _, t := range tables {
		tbl, err := e.outFreezer.EnsureTable(t.name, t.ext)
		if err != nil {
			return err
		}
		for tbl.Items() < startBlock {
			if err := tbl.Append(tbl.Items(), []byte{}); err != nil {
				return fmt.Errorf("pad %s at %d: %w", t.name, tbl.Items(), err)
			}
		}
	}
	return nil
}

// cacheHeader adds a header to the cache, evicting old entries.
func (e *Executor) cacheHeader(num uint64, h *block.Header) {
	e.headerCache[num] = h
	if num >= 256 {
		delete(e.headerCache, num-256)
	}
}

// writeOutputs writes execution results to the output freezer.
// All 6 tables must receive an append for every block to maintain alignment.
func (e *Executor) writeOutputs(blockNum uint64, result *BlockResult, writer state.WriterWithChangeSets, witness *WitnessStateReader, dbTx kv.Tx) error {
	of := e.outFreezer
	appendTo := func(table, ext string, data []byte) error {
		tbl, err := of.EnsureTable(table, ext)
		if err != nil {
			return err
		}
		return tbl.Append(blockNum, data)
	}

	// 1. Receipts.
	var receiptsRLP []byte
	if len(result.Receipts) > 0 {
		var err error
		receiptsRLP, err = rlp.EncodeToBytes(result.Receipts)
		if err != nil {
			return fmt.Errorf("encode receipts: %w", err)
		}
	}
	if err := appendTo(freezer.TableReceipts, "c", receiptsRLP); err != nil {
		return fmt.Errorf("receipts: %w", err)
	}

	// 2. Senders.
	sendersBuf := make([]byte, 0, len(result.Senders)*20)
	for _, s := range result.Senders {
		sendersBuf = append(sendersBuf, s[:]...)
	}
	if err := appendTo(freezer.TableSenders, "c", sendersBuf); err != nil {
		return fmt.Errorf("senders: %w", err)
	}

	// 3-4. Changesets + Leaves journal (single csw extraction).
	var accCSBytes, stoCSBytes, leavesData []byte
	if psw, ok := writer.(*state.PlainStateWriter); ok {
		if csw := psw.ChangeSetWriter(); csw != nil {
			accCS, err := csw.GetAccountChanges()
			if err != nil {
				return fmt.Errorf("get account changes: %w", err)
			}
			stoCS, err := csw.GetStorageChanges()
			if err != nil {
				return fmt.Errorf("get storage changes: %w", err)
			}

			accCSBytes = encodeChanges(accCS)
			stoCSBytes = encodeChanges(stoCS)

			leavesData = EncodeLeavesJournal(accCS, stoCS,
				func(addr types.Address) *account.StateAccount {
					v, err := dbTx.GetOne("Account", addr[:])
					if err != nil || v == nil {
						return nil
					}
					var a account.StateAccount
					if a.DecodeForStorage(v) != nil {
						return nil
					}
					return &a
				},
				func(addr types.Address, key types.Hash) []byte {
					compositeKey := make([]byte, 54)
					copy(compositeKey[:20], addr[:])
					copy(compositeKey[22:], key[:])
					v, _ := dbTx.GetOne("Storage", compositeKey)
					return v
				},
			)
		}
	}
	// Always append (empty if no csw) to maintain table alignment.
	if err := appendTo(freezer.TableAccountChanges, "c", accCSBytes); err != nil {
		return fmt.Errorf("account changes: %w", err)
	}
	if err := appendTo(freezer.TableStorageChanges, "c", stoCSBytes); err != nil {
		return fmt.Errorf("storage changes: %w", err)
	}
	if err := appendTo(freezer.TableLeavesJournal, "c", leavesData); err != nil {
		return fmt.Errorf("leaves journal: %w", err)
	}

	// 5. Block witness.
	var witnessData []byte
	if witness != nil {
		witnessData = witness.Encode()
	}
	if err := appendTo(freezer.TableBlockWitness, "c", witnessData); err != nil {
		return fmt.Errorf("block witness: %w", err)
	}

	return nil
}

// encodeChanges serializes a ChangeSet to flat bytes using RLP.
func encodeChanges(cs *changeset.ChangeSet) []byte {
	if cs == nil || cs.Len() == 0 {
		return []byte{}
	}
	// Encode as RLP: [[key, value], [key, value], ...]
	pairs := make([][]interface{}, cs.Len())
	for i, c := range cs.Changes {
		pairs[i] = []interface{}{c.Key, c.Value}
	}
	data, _ := rlp.EncodeToBytes(pairs)
	return data
}
