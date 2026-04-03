// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"fmt"
	"time"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/changeset"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"

	iinternal "github.com/n42blockchain/N42/internal"
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
	defer tx.Rollback()

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

	// Pad output freezer to match startBlock (freezer requires sequential item 0,1,2...).
	if e.outFreezer != nil {
		if err := e.padOutputFreezer(startBlock); err != nil {
			return fmt.Errorf("pad output freezer: %w", err)
		}
	}

	startTime := time.Now()

	for blockNum := startBlock; blockNum <= endBlock; blockNum++ {
		if ctx.Err() != nil {
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
				return err
			}
			// No defer — tx is committed or rolled back explicitly.
		}
	}

	// Final commit with progress.
	if err := WriteProgress(tx, endBlock); err != nil {
		return fmt.Errorf("write final progress: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}

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
	// Wrap reader with WitnessStateReader to capture block witness.
	plainReader := state.NewPlainStateReader(tx)
	var witnessReader *WitnessStateReader
	var reader state.StateReader = plainReader
	if e.outFreezer != nil {
		witnessReader = NewWitnessStateReader(plainReader)
		reader = witnessReader
	}
	writer := state.NewPlainStateWriter(tx, tx, blockNum)
	ibs := state.New(reader)

	shouldVerify := e.cfg.VerifyInterval > 0 && blockNum%e.cfg.VerifyInterval == 0

	// 3. Process block (DAO fork, system contracts, EVM).
	result, err := e.processBlock(tx, header, body, ibs, writer)
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

	// 5. Commit state changes to writer.
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

	// 6. Write execution outputs to output freezer.
	if e.outFreezer != nil {
		if err := e.writeOutputs(blockNum, result, writer, witnessReader, tx); err != nil {
			return fmt.Errorf("write outputs: %w", err)
		}
	}

	// 7. Verify state root (if enabled).
	// Use full re-hash from Account/Storage tables.
	if shouldVerify {
		computedRoot, err := FullStateRootVerify(tx)
		if err != nil {
			return fmt.Errorf("state root computation: %w", err)
		}
		if computedRoot != header.Root {
			return fmt.Errorf("state root mismatch at block %d: computed %s, expected %s",
				blockNum, computedRoot.Hex(), header.Root.Hex())
		}
		log.Info("State root verified", "block", blockNum, "root", header.Root.Hex())
	}

	return nil
}

// blockResult holds per-block execution outputs for freezer storage.
type blockResult struct {
	GasUsed  uint64
	Receipts block.Receipts
	Senders  []types.Address
}

// processBlock runs the EVM for all transactions in a block.
func (e *Executor) processBlock(tx kv.RwTx, header *block.Header, body *GethBodyResult, ibs *state.IntraBlockState, writer state.WriterWithChangeSets) (*blockResult, error) {
	txs := body.Transactions
	usedGas := new(uint64)
	gp := new(common.GasPool)
	gp.AddGas(header.GasLimit)

	cfg := vm2.Config{}

	// DAO fork.
	if e.chainCfg.DAOForkSupport && e.chainCfg.DAOForkBlock != nil &&
		e.chainCfg.DAOForkBlock.Cmp(header.Number.ToBig()) == 0 {
		misc.ApplyDAOHardFork(ibs)
	}

	// Pre-block system calls (beacon root, etc.).
	if err := iinternal.ProcessExecutionBlockStart(nil, e.chainCfg, ibs, header, e.engine); err != nil {
		return nil, err
	}

	noop := state.NewNoopWriter()
	blockHashFunc := e.makeBlockHashFunc(header)
	blockHash := header.Hash()

	var receipts block.Receipts
	senders := make([]types.Address, 0, len(txs))

	for i, txn := range txs {
		ibs.Prepare(txn.Hash(), blockHash, i)
		receipt, _, err := iinternal.ApplyTransaction(
			e.chainCfg, blockHashFunc, e.engine, nil, gp,
			ibs, noop, header, txn, usedGas, cfg,
		)
		if err != nil {
			return nil, fmt.Errorf("tx %d: %w", i, err)
		}
		if receipt != nil {
			receipts = append(receipts, receipt)
		}

		// Recover sender from signature.
		signer := transaction.MakeSigner(e.chainCfg, header.Number.ToBig())
		sender, err := transaction.Sender(signer, txn)
		if err != nil {
			return nil, fmt.Errorf("tx %d sender: %w", i, err)
		}
		senders = append(senders, sender)
	}

	// Post-block system calls.
	if _, err := iinternal.ProcessExecutionBlockEnd(nil, e.chainCfg, ibs, header, e.engine); err != nil {
		return nil, err
	}

	// Finalize (block rewards + uncle rewards).
	var uncleHeaders []block.IHeader
	for _, u := range body.Uncles {
		uncleHeaders = append(uncleHeaders, u)
	}
	if _, _, err := e.engine.Finalize(nil, header, ibs, txs, uncleHeaders); err != nil {
		return nil, fmt.Errorf("finalize: %w", err)
	}

	return &blockResult{GasUsed: *usedGas, Receipts: receipts, Senders: senders}, nil
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
func (e *Executor) writeOutputs(blockNum uint64, result *blockResult, writer state.WriterWithChangeSets, witness *WitnessStateReader, tx kv.Tx) error {
	of := e.outFreezer

	// 1. Receipts: RLP-encode the list.
	receiptsTable, err := of.EnsureTable(freezer.TableReceipts, "c")
	if err != nil {
		return err
	}
	var receiptsRLP []byte
	if len(result.Receipts) > 0 {
		receiptsRLP, err = rlp.EncodeToBytes(result.Receipts)
		if err != nil {
			return fmt.Errorf("encode receipts: %w", err)
		}
	}
	if err := receiptsTable.Append(blockNum, receiptsRLP); err != nil {
		return fmt.Errorf("append receipts: %w", err)
	}

	// 2. Senders: concatenate 20-byte addresses.
	sendersTable, err := of.EnsureTable(freezer.TableSenders, "c")
	if err != nil {
		return err
	}
	sendersBuf := make([]byte, 0, len(result.Senders)*20)
	for _, s := range result.Senders {
		sendersBuf = append(sendersBuf, s[:]...)
	}
	if err := sendersTable.Append(blockNum, sendersBuf); err != nil {
		return fmt.Errorf("append senders: %w", err)
	}

	// 3. Changesets: get from PlainStateWriter and RLP-encode.
	if psw, ok := writer.(*state.PlainStateWriter); ok {
		csw := psw.ChangeSetWriter()
		if csw != nil {
			accCS, _ := csw.GetAccountChanges()
			accTable, err := of.EnsureTable(freezer.TableAccountChanges, "c")
			if err != nil {
				return err
			}
			accBytes := encodeChanges(accCS)
			if err := accTable.Append(blockNum, accBytes); err != nil {
				return fmt.Errorf("append account changes: %w", err)
			}

			stoCS, _ := csw.GetStorageChanges()
			stoTable, err := of.EnsureTable(freezer.TableStorageChanges, "c")
			if err != nil {
				return err
			}
			stoBytes := encodeChanges(stoCS)
			if err := stoTable.Append(blockNum, stoBytes); err != nil {
				return fmt.Errorf("append storage changes: %w", err)
			}
		}
	}

	// 4. Leaves journal: per-block trie leaf changes (hashed keys + new values).
	if psw, ok := writer.(*state.PlainStateWriter); ok {
		csw := psw.ChangeSetWriter()
		if csw != nil {
			accCS, _ := csw.GetAccountChanges()
			stoCS, _ := csw.GetStorageChanges()

			leavesData := EncodeLeavesJournal(accCS, stoCS,
				func(addr types.Address) *account.StateAccount {
					v, err := tx.GetOne("Account", addr[:])
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
					// incarnation = 0 for simple lookup
					copy(compositeKey[22:], key[:])
					v, _ := tx.GetOne("Storage", compositeKey)
					return v
				},
			)
			leavesTable, err := of.EnsureTable(freezer.TableLeavesJournal, "c")
			if err != nil {
				return err
			}
			if err := leavesTable.Append(blockNum, leavesData); err != nil {
				return fmt.Errorf("append leaves journal: %w", err)
			}
		}
	}

	// 5. Block witness: minimal state access set for stateless replay (no code).
	if witness != nil {
		witnessTable, err := of.EnsureTable(freezer.TableBlockWitness, "c")
		if err != nil {
			return err
		}
		if err := witnessTable.Append(blockNum, witness.Encode()); err != nil {
			return fmt.Errorf("append block witness: %w", err)
		}
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
