// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
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
	// Note: History bitmaps (AccountsHistory/StorageHistory) and log indices
	// (TxLookup/LogTopicIndex/LogAddressIndex) are NOT written during sync.
	// They are rebuilt as a batch stage after sync completes.
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

	// Write buffer: accumulates state changes in memory, flushed to MDBX
	// only at commit boundaries. Eliminates per-block MDBX Put overhead.
	stateBuf         *state.PlainStateBuffer
	lastProgressTime time.Time
	prefetcher       *prefetcher

	// Pre-computed senders from sender-recovery stage.
	senderFreezer *freezer.Freezer
	senderTable   *freezer.FreezerTable
	senderMisses  uint64

	// Output batcher: accumulates entries, writes in batches.
	outBatcher *outputBatcher

	// History accumulators: build RecSplit history segments inline during execution.
	accHistAccum *cscompact.HistoryAccumulator
	stoHistAccum *cscompact.HistoryAccumulator

	// Timing collection for P50/P99 analysis.
	timingSamples []timingSample
	timingWindow  int // samples per window (default 1000)
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
		stateBuf:    state.NewPlainStateBuffer(),
	}
}

// SetSenderFreezer sets the pre-computed senders freezer (from sender-recovery stage).
func (e *Executor) SetSenderFreezer(f *freezer.Freezer) {
	e.senderFreezer = f
	e.senderTable = f.Table("senders")
}

// SetHistoryDir enables inline history segment building during execution.
// Segments are flushed to outputDir/account_hist/ and outputDir/storage_hist/.
func (e *Executor) SetHistoryDir(outputDir string) error {
	accAccum, err := cscompact.NewAccountHistoryAccumulator(outputDir)
	if err != nil {
		return fmt.Errorf("init account history accumulator: %w", err)
	}
	stoAccum, err := cscompact.NewStorageHistoryAccumulator(outputDir)
	if err != nil {
		accAccum.Close(0)
		return fmt.Errorf("init storage history accumulator: %w", err)
	}
	e.accHistAccum = accAccum
	e.stoHistAccum = stoAccum
	log.Info("History accumulators initialized",
		"dir", outputDir,
		"existingAccSegs", accAccum.SegmentCount(),
		"existingStoSegs", stoAccum.SegmentCount())
	return nil
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

	// Initialize output batcher and align tables to startBlock.
	if e.outFreezer != nil && !e.cfg.NoOutputs {
		batcher, err := newOutputBatcher(e.outFreezer)
		if err != nil {
			return fmt.Errorf("init output batcher: %w", err)
		}
		e.outBatcher = batcher
		defer batcher.Close()
		if err := batcher.alignOnResume(startBlock); err != nil {
			return fmt.Errorf("align output tables: %w", err)
		}
		// Pad senders if not pre-computed.
		if e.senderTable == nil {
			if err := e.padSendersTable(startBlock); err != nil {
				return fmt.Errorf("pad senders: %w", err)
			}
		}
	}

	startTime := time.Now()
	e.lastProgressTime = startTime

	// Start background prefetcher to warm MDBX page cache.
	e.prefetcher = newPrefetcher(ctx, e.freezer, e.db, e.stateBuf, e.chainCfg)
	e.prefetcher.start()
	defer e.prefetcher.stop()

	for blockNum := startBlock; blockNum <= endBlock; blockNum++ {
		if ctx.Err() != nil {
			log.Info("Shutting down executor", "lastBlock", blockNum-1)
			// Save progress for the last committed block before exit.
			return ctx.Err()
		}

		// Prefetch next block's state while we execute this one.
		if blockNum+1 <= endBlock {
			e.prefetcher.prefetchBlock(blockNum + 1)
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
			// Flush only full batches — keep partial batch in memory so entries
			// remain aligned within their 64-entry batch boundary.
			if e.outBatcher != nil {
				if _, err := e.outBatcher.flushFullBatches(); err != nil {
					return fmt.Errorf("flush output batcher at block %d: %w", blockNum, err)
				}
			}
			// Flush write buffer to MDBX.
			bufAccs, bufStos := e.stateBuf.Stats()
			cacheHits, cacheMisses := e.stateBuf.CacheStats()
			t0Flush := time.Now()
			if err := e.stateBuf.FlushToMDBX(tx); err != nil {
				return fmt.Errorf("flush buffer at block %d: %w", blockNum, err)
			}
			flushDur := time.Since(t0Flush)
			e.stateBuf.Clear()

			// Save progress before commit.
			if err := WriteProgress(tx, blockNum); err != nil {
				return fmt.Errorf("write progress at block %d: %w", blockNum, err)
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit at block %d: %w", blockNum, err)
			}
			now := time.Now()
			intervalBlks := e.cfg.CommitInterval
			intervalSec := now.Sub(e.lastProgressTime).Seconds()
			if intervalSec < 0.001 {
				intervalSec = 0.001
			}
			blkPerSec := float64(intervalBlks) / intervalSec
			e.lastProgressTime = now
			hitRate := float64(0)
			if cacheHits+cacheMisses > 0 {
				hitRate = float64(cacheHits) / float64(cacheHits+cacheMisses) * 100
			}
			fields := []interface{}{
				"block", blockNum,
				"blk/s", fmt.Sprintf("%.0f", blkPerSec),
				"elapsed", now.Sub(startTime).Truncate(time.Second),
				"bufFlush", flushDur.Truncate(time.Millisecond),
				"bufAccs", bufAccs,
				"bufStos", bufStos,
				"cacheHit%", fmt.Sprintf("%.1f", hitRate),
			}
			if e.senderMisses > 0 {
				fields = append(fields, "senderMiss", e.senderMisses)
				e.senderMisses = 0
			}
			log.Info("EthEL progress", fields...)

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

	// Final flush + commit.
	if e.outBatcher != nil {
		if err := e.outBatcher.flushAll(); err != nil {
			return fmt.Errorf("flush output batcher final: %w", err)
		}
	}
	// Flush remaining history accumulator data.
	if e.accHistAccum != nil {
		if err := e.accHistAccum.Close(endBlock); err != nil {
			log.Warn("Failed to flush account history accumulator", "err", err)
		}
	}
	if e.stoHistAccum != nil {
		if err := e.stoHistAccum.Close(endBlock); err != nil {
			log.Warn("Failed to flush storage history accumulator", "err", err)
		}
	}
	if err := e.stateBuf.FlushToMDBX(tx); err != nil {
		return fmt.Errorf("flush buffer final: %w", err)
	}
	e.stateBuf.Clear()
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

	// 2. Set up state reader/writer with write buffer.
	// Reader checks in-memory buffer first, falls through to MDBX.
	bufReader := state.NewBufferedPlainStateReader(e.stateBuf, tx)
	var witnessReader *WitnessStateReader
	var reader state.StateReader = bufReader
	if e.outFreezer != nil && !e.cfg.NoOutputs {
		witnessReader = NewWitnessStateReader(bufReader)
		reader = witnessReader
	}
	// Writer goes to in-memory buffer (no MDBX Put per block).
	writer := state.NewBufferedPlainStateWriter(e.stateBuf, tx, blockNum)
	ibs := state.New(reader)

	shouldVerify := e.cfg.VerifyInterval > 0 && blockNum%e.cfg.VerifyInterval == 0

	// Attach HashOnlyComputer to keep HashedAccounts in sync incrementally.
	if e.cfg.VerifyInterval > 0 {
		ibs.SetRootComputer(NewHashOnlyComputer(tx))
	}

	t1 := time.Now()
	// 3. Process block (DAO fork, system contracts, EVM).
	// Load pre-computed senders if available and in range.
	var senders []types.Address
	if e.senderTable != nil && blockNum < e.senderTable.Items() {
		senderData, err := e.senderTable.Retrieve(blockNum)
		if err != nil {
			if e.senderMisses == 0 {
				log.Warn("Sender retrieve error", "block", blockNum, "err", err, "items", e.senderTable.Items())
			}
		} else {
			txCount := len(body.Transactions)
			senderCount := len(senderData) / 20
			if senderCount == txCount {
				senders = make([]types.Address, senderCount)
				for i := 0; i < senderCount; i++ {
					copy(senders[i][:], senderData[i*20:(i+1)*20])
				}
			} else if e.senderMisses == 0 {
				log.Warn("Sender count mismatch", "block", blockNum, "txs", txCount, "senders", senderCount, "dataLen", len(senderData))
			}
		}
	}
	if e.senderTable != nil && senders == nil && len(body.Transactions) > 0 {
		e.senderMisses++
	}

	result, err := e.processBlock(header, body, ibs, senders)
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
	// 6. Commit state changes to in-memory buffer (no MDBX writes).
	rules := e.chainCfg.Rules(header.Number.Uint64())
	if err := ibs.CommitBlock(rules, writer); err != nil {
		return fmt.Errorf("commit block state: %w", err)
	}
	t3 := time.Now()

	// 7. Write execution outputs to output freezer.
	if e.outFreezer != nil && !e.cfg.NoOutputs {
		if err := e.writeOutputs(blockNum, result, writer, witnessReader, tx); err != nil {
			return fmt.Errorf("write outputs: %w", err)
		}
		// Advance history accumulators (flush full segments automatically).
		if e.accHistAccum != nil {
			if err := e.accHistAccum.AdvanceBlock(blockNum); err != nil {
				return fmt.Errorf("account history advance: %w", err)
			}
		}
		if e.stoHistAccum != nil {
			if err := e.stoHistAccum.AdvanceBlock(blockNum); err != nil {
				return fmt.Errorf("storage history advance: %w", err)
			}
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

	// Collect timing samples for P50/P99 analysis.
	e.collectTiming(blockNum, timingSample{
		read: t1.Sub(t0), evm: t2.Sub(t1), commit: t3.Sub(t2),
		outputs: t5.Sub(t3), total: t5.Sub(t0),
		txCount: len(body.Transactions), gasUsed: result.GasUsed,
	})

	return nil
}

// BlockResult is defined in process.go. This comment prevents confusion.
// processBlock delegates to the shared ProcessBlock function.
func (e *Executor) processBlock(header *block.Header, body *GethBodyResult, ibs *state.IntraBlockState, senders []types.Address) (*BlockResult, error) {
	var uncles []block.IHeader
	for _, u := range body.Uncles {
		uncles = append(uncles, u)
	}
	return ProcessBlock(e.chainCfg, e.engine, header, body.Transactions, uncles, ibs, e.makeBlockHashFunc(header), senders)
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

// padSendersTable pads the senders table for blocks 0..startBlock-1
// when senders are NOT pre-computed by the sender-recovery stage.
// If the table already has data (from a prior sender-recovery run),
// existing data is preserved — only gaps below startBlock are filled.
func (e *Executor) padSendersTable(startBlock uint64) error {
	tbl, err := e.outFreezer.EnsureTableCompressed(freezer.TableSenders, "c")
	if err != nil {
		return err
	}
	items := tbl.Items()
	if items >= startBlock {
		log.Info("Senders table already at or past startBlock, no padding needed",
			"items", items, "startBlock", startBlock)
		return nil
	}
	log.Info("Padding senders table", "from", items, "to", startBlock, "gap", startBlock-items)
	return padTableTo(tbl, startBlock, e.outBatcher.enc)
}

// cacheHeader adds a header to the cache, evicting old entries.
func (e *Executor) cacheHeader(num uint64, h *block.Header) {
	e.headerCache[num] = h
	if num >= 256 {
		delete(e.headerCache, num-256)
	}
}

type timingSample struct {
	read, evm, commit, outputs, total time.Duration
	txCount                           int
	gasUsed                           uint64
}

func (e *Executor) collectTiming(blockNum uint64, s timingSample) {
	if e.timingWindow == 0 {
		e.timingWindow = 10000
	}
	e.timingSamples = append(e.timingSamples, s)
	if len(e.timingSamples) >= e.timingWindow {
		e.reportTimings(blockNum)
		e.timingSamples = e.timingSamples[:0]
	}
}

func (e *Executor) reportTimings(blockNum uint64) {
	s := e.timingSamples
	n := len(s)
	if n == 0 {
		return
	}

	sort.Slice(s, func(i, j int) bool { return s[i].total < s[j].total })

	p50 := s[n*50/100]
	p99 := s[n*99/100]
	worst := s[n-1]

	var totalGas uint64
	var totalTx int
	for _, v := range s {
		totalGas += v.gasUsed
		totalTx += v.txCount
	}

	ms := func(d time.Duration) string { return fmt.Sprintf("%.1f", float64(d.Microseconds())/1000) }
	log.Info("P50/P99",
		"block", blockNum,
		"tx", totalTx/n,
		"gas", totalGas/uint64(n),
		"P50", ms(p50.total),
		"P50evm", ms(p50.evm),
		"P99", ms(p99.total),
		"P99evm", ms(p99.evm),
		"P99commit", ms(p99.commit),
		"P99out", ms(p99.outputs),
		"worst", ms(worst.total),
	)
}

// writeOutputs accumulates block results into the output batcher.
// Batches are flushed automatically when full (execBatchSize entries).
func (e *Executor) writeOutputs(blockNum uint64, result *BlockResult, writer *state.BufferedPlainStateWriter, witness *WitnessStateReader, dbTx kv.Tx) error {
	b := e.outBatcher

	// 1. Receipts.
	receiptsData := EncodeReceiptsCompact(result.Receipts)
	if err := b.addEntry(freezer.TableReceipts, "c", receiptsData); err != nil {
		return fmt.Errorf("receipts: %w", err)
	}

	// 2. Changesets + Leaves journal.
	var accCSBytes, stoCSBytes, leavesData []byte
	if blockNum == 0 {
		// Genesis block: journal contains ALL genesis accounts/storage
		// so that the journal alone can reconstruct full PlainState.
		// No changesets for genesis (alloc was loaded via InitEthGenesisState).
		var err error
		leavesData, err = EncodeGenesisJournal(dbTx)
		if err != nil {
			return fmt.Errorf("encode genesis journal: %w", err)
		}
	} else {
		csw := writer.ChangeSetWriter()
		if csw != nil {
			accCS, err := csw.GetAccountChanges()
			if err != nil {
				return fmt.Errorf("get account changes: %w", err)
			}
			stoCS, err := csw.GetStorageChanges()
			if err != nil {
				return fmt.Errorf("get storage changes: %w", err)
			}

			accCSBytes = EncodeAccountChanges(accCS)
			stoCSBytes = EncodeStorageChanges(stoCS)

			// Feed changed keys to history accumulators.
			if e.accHistAccum != nil {
				for _, c := range accCS.Changes {
					e.accHistAccum.AddAccountKey(c.Key, blockNum)
				}
			}
			if e.stoHistAccum != nil {
				for _, c := range stoCS.Changes {
					// StorageChangeSet key = addr(20)+incarnation(8), value = slot(32)+oldValue
					if len(c.Key) >= 20 && len(c.Value) >= 32 {
						e.stoHistAccum.AddStorageKey(c.Key[:20], c.Value[:32], blockNum)
					}
				}
			}

			// Use buffered reader for current values (buffer → MDBX fallthrough).
			bufReader := state.NewBufferedPlainStateReader(e.stateBuf, dbTx)
			leavesData = EncodeLeavesJournal(accCS, stoCS,
				func(addr types.Address) *account.StateAccount {
					a, err := bufReader.ReadAccountData(addr)
					if err != nil {
						return nil
					}
					return a
				},
				func(addr types.Address, key types.Hash) []byte {
					v, err := bufReader.ReadAccountStorage(addr, 0, &key)
					if err != nil {
						return nil
					}
					return v
				},
			)
		}
	}
	if err := b.addEntry(freezer.TableAccountChanges, "c", accCSBytes); err != nil {
		return fmt.Errorf("account changes: %w", err)
	}
	if err := b.addEntry(freezer.TableStorageChanges, "c", stoCSBytes); err != nil {
		return fmt.Errorf("storage changes: %w", err)
	}
	if err := b.addEntry(freezer.TableLeavesJournal, "c", leavesData); err != nil {
		return fmt.Errorf("leaves journal: %w", err)
	}

	// 3. Block witness.
	var witnessData []byte
	if witness != nil {
		witnessData = witness.Encode()
	}
	if err := b.addEntry(freezer.TableBlockWitness, "c", witnessData); err != nil {
		return fmt.Errorf("block witness: %w", err)
	}

	// Flush complete batches.
	if _, err := b.flushFullBatches(); err != nil {
		return fmt.Errorf("flush batches: %w", err)
	}

	return nil
}

