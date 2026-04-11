// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// executor.go — batch block executor that drives the eth-el replay.
//
// Executor reads headers, bodies, senders and receipts from a Geth-format
// input Freezer, re-executes every transaction through the shared
// ProcessBlock path, and writes receipts, senders, changesets, and the
// leaves journal to an output Freezer. A PlainStateBuffer accumulates
// state mutations between commit boundaries so that MDBX only sees one
// batch per CommitInterval. VerifyInterval toggles periodic state-root
// verification; LeavesOnly and NoOutputs trim the output set for special
// runs such as journal regeneration.

package ethel

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/rlp"
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
	// LeavesOnly if true, only write leaves_journal and block_witness (skip receipts, changesets, senders).
	LeavesOnly bool
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
	senderStore   *SenderSegmentReader // SegmentStore-based senders (chain/senders.cidx)
	senderMisses  uint64

	// Compact readers for columnar headers/bodies (alternative to Geth freezer).
	compactHeaders *HeaderCompactReader
	compactBodies  *BodyCompactReader

	// Output batcher: accumulates entries, writes in batches.
	outBatcher *outputBatcher

	// Timing collection for P50/P99 analysis.
	timingSamples []timingSample
	timingWindow  int // samples per window (default 1000)
}

// NewExecutor creates a new block executor.
func NewExecutor(f *freezer.Freezer, db kv.RwDB, chainCfg *params.ChainConfig, engine consensus.Engine, cfg ExecutorConfig, outFreezer *freezer.Freezer) *Executor {
	if cfg.CommitInterval == 0 {
		cfg.CommitInterval = 10000
	}
	// Ensure Geth input freezer has headers/bodies tables open
	// (coreTableSpecs is empty to protect compact files in chain/).
	if f != nil {
		f.EnsureTable("headers", "c")
		f.EnsureTable("bodies", "c")
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

// SetSenderStore sets the SegmentStore-based senders reader (chain/senders.cidx).
func (e *Executor) SetSenderStore(r *SenderSegmentReader) {
	e.senderStore = r
}

// SetCompactReaders sets columnar header/body readers (alternative to Geth freezer).
func (e *Executor) SetCompactReaders(hr *HeaderCompactReader, br *BodyCompactReader) {
	e.compactHeaders = hr
	e.compactBodies = br
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
		if err := batcher.alignOnResume(startBlock, e.cfg.LeavesOnly); err != nil {
			return fmt.Errorf("align output tables: %w", err)
		}
		// Senders/receipts deprecated: no padding or per-block writes.
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
		if e.prefetcher != nil && blockNum+1 <= endBlock {
			e.prefetcher.prefetchBlock(blockNum + 1)
		}

		if err := e.executeBlock(ctx, tx, blockNum); err != nil {
			if e.cfg.SkipErrors {
				log.Warn("Block execution error (skipped)", "block", blockNum, "err", err)
				continue
			}
			return fmt.Errorf("block %d: %w", blockNum, err)
		}

		// Periodic flush (within same tx) and conditional commit.
		if blockNum > 0 && blockNum%e.cfg.CommitInterval == 0 {
			// Flush output batches.
			if e.outBatcher != nil {
				if _, err := e.outBatcher.flushFullBatches(); err != nil {
					return fmt.Errorf("flush output batcher at block %d: %w", blockNum, err)
				}
			}
			// Flush write buffer to MDBX (within same tx — NOT committed yet).
			bufAccs, bufStos := e.stateBuf.Stats()
			cacheHits, cacheMisses := e.stateBuf.CacheStats()
			t0Flush := time.Now()
			if err := e.stateBuf.FlushToMDBX(tx); err != nil {
				return fmt.Errorf("flush buffer at block %d: %w", blockNum, err)
			}
			flushDur := time.Since(t0Flush)
			e.stateBuf.Clear()

			// Progress log.
			now := time.Now()
			intervalSec := now.Sub(e.lastProgressTime).Seconds()
			if intervalSec < 0.001 { intervalSec = 0.001 }
			blkPerSec := float64(e.cfg.CommitInterval) / intervalSec
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
				"bufAccs", bufAccs, "bufStos", bufStos,
				"cacheHit%", fmt.Sprintf("%.1f", hitRate),
			}
			if e.senderMisses > 0 {
				fields = append(fields, "senderMiss", e.senderMisses)
				e.senderMisses = 0
			}
			log.Info("EthEL progress", fields...)

			// Always commit at commitInterval for safety.
			if err := WriteProgress(tx, blockNum); err != nil {
				return fmt.Errorf("write progress at block %d: %w", blockNum, err)
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit at block %d: %w", blockNum, err)
			}
			tx, err = e.db.BeginRw(ctx)
			if err != nil {
				cleanup = func() {}
				return err
			}
			cleanup = func() { tx.Rollback() }

			// Verify state root at verify boundaries.
			// On success: stop so user can backup MDBX.
			// On failure: report and stop (state already committed — user restores backup).
			if e.cfg.VerifyInterval > 0 && blockNum%e.cfg.VerifyInterval == 0 {
				blockRoot, verifyErr := VerifyStateRoot(tx)
				if verifyErr != nil {
					return fmt.Errorf("state root verify at block %d: %w", blockNum, verifyErr)
				}
				hdr, hErr := e.readHeader(blockNum)
				if hErr != nil {
					return fmt.Errorf("read header for verify at block %d: %w", blockNum, hErr)
				}
				if blockRoot != hdr.Root {
					log.Error("State root MISMATCH",
						"block", blockNum,
						"computed", blockRoot.Hex(),
						"expected", hdr.Root.Hex())
					return fmt.Errorf("state root mismatch at block %d — restore backup and re-run with smaller --verify",
						blockNum)
				}
				log.Info("State root verified", "block", blockNum,
					"root", hdr.Root.Hex())
			}
		}
	}

	// Final flush + commit.
	if e.outBatcher != nil {
		if err := e.outBatcher.flushAll(); err != nil {
			return fmt.Errorf("flush output batcher final: %w", err)
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

	// State root verification moved to main loop (Run) for rollback support.

	t1 := time.Now()
	// 3. Process block (DAO fork, system contracts, EVM).
	var senders []types.Address
	senders = e.loadSenders(blockNum, len(body.Transactions))

	if false {
		var uncles []block.IHeader
		for _, u := range body.Uncles {
			uncles = append(uncles, u)
		}
		if err := RunTxDiff(e.chainCfg, e.engine, header, body.Transactions, uncles,
			e.stateBuf, tx, e.makeBlockHashFunc(header), senders); err != nil {
			return err
		}
		return fmt.Errorf("TX diff completed at block %d — stopping", blockNum)
	}

	result, err := e.processBlock(header, body, ibs, senders, writer)
	if err != nil {
		return err
	}

	// 4. Verify gas used.
	if result.GasUsed != header.GasUsed {
		// Diagnostic: locate the FIRST mis-charged transaction by comparing
		// per-tx cumulative gas against geth's stored receipts.
		e.dumpGasMismatch(blockNum, header, body, result)
		if e.cfg.SkipErrors {
			log.Warn("Gas mismatch (skipped)",
				"block", blockNum, "got", result.GasUsed, "want", header.GasUsed,
				"diff", int64(header.GasUsed)-int64(result.GasUsed))
		} else {
			return fmt.Errorf("gas mismatch: got %d, want %d", result.GasUsed, header.GasUsed)
		}
	}

	t2 := time.Now()
	// 6. Commit state changes to in-memory buffer.
	// Capture wipes length BEFORE CommitBlock so we can extract per-block wipes.
	wipesBefore := len(e.stateBuf.ContractWipes())
	rules := e.chainCfg.Rules(header.Number.Uint64())
	if err := ibs.CommitBlock(rules, writer); err != nil {
		return fmt.Errorf("commit block state: %w", err)
	}
	t3 := time.Now()

	// 7. Write execution outputs to output freezer.
	if e.outFreezer != nil && !e.cfg.NoOutputs {
		// Extract only THIS block's wipes (not accumulated from prior blocks).
		allWipes := e.stateBuf.ContractWipes()
		blockWipes := allWipes[wipesBefore:]
		if err := e.writeOutputs(blockNum, result, writer, witnessReader, tx, blockWipes); err != nil {
			return fmt.Errorf("write outputs: %w", err)
		}
	}

	// 8. State root verification is handled by the main loop (Run)
	//    after commit, to enable rollback on mismatch.

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
func (e *Executor) processBlock(header *block.Header, body *GethBodyResult, ibs *state.IntraBlockState, senders []types.Address, writer state.StateWriter) (*BlockResult, error) {
	var uncles []block.IHeader
	for _, u := range body.Uncles {
		uncles = append(uncles, u)
	}
	return ProcessBlock(e.chainCfg, e.engine, header, body.Transactions, uncles, ibs, e.makeBlockHashFunc(header), senders, writer)
}

// readHeader reads a header from compact reader or Geth freezer.
func (e *Executor) readHeader(blockNum uint64) (*block.Header, error) {
	if e.compactHeaders != nil {
		return e.compactHeaders.ReadHeader(blockNum)
	}
	data, err := e.freezer.Ancient(freezer.TableHeaders, blockNum)
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	return DecodeGethHeader(data)
}

// readBody reads a body from compact reader or Geth freezer.
// Falls back to Geth freezer if compact body looks incomplete (e.g. missing uncles).
func (e *Executor) readBody(blockNum uint64) (*GethBodyResult, error) {
	if e.compactBodies != nil {
		decoded, err := e.compactBodies.ReadBody(blockNum)
		if err == nil && (len(decoded.Txs) > 0 || len(decoded.UncleRLP) > 0 || len(decoded.Withdrawals) > 0) {
			result := &GethBodyResult{
				Transactions: decoded.Txs,
				Withdrawals:  decoded.Withdrawals,
			}
			for _, rlpBytes := range decoded.UncleRLP {
				var uncle block.Header
				if err := rlp.DecodeBytes(rlpBytes, &uncle); err == nil {
					result.Uncles = append(result.Uncles, &uncle)
				}
			}
			return result, nil
		}
		// Compact returned empty body — may be missing uncles. Fall through to Geth.
	}
	data, err := e.freezer.Ancient(freezer.TableBodies, blockNum)
	if err != nil {
		return nil, fmt.Errorf("read body %d: %w", blockNum, err)
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
// loadSenders tries SegmentStore first, then freezer, returns nil if unavailable.
func (e *Executor) loadSenders(blockNum uint64, txCount int) []types.Address {
	// Try SegmentStore (chain/senders.cidx).
	if e.senderStore != nil && blockNum < e.senderStore.MaxBlock() {
		data, err := e.senderStore.ReadBlock(blockNum)
		if err == nil {
			senderCount := len(data) / 20
			if senderCount == txCount && txCount > 0 {
				senders := make([]types.Address, senderCount)
				for i := 0; i < senderCount; i++ {
					copy(senders[i][:], data[i*20:(i+1)*20])
				}
				return senders
			}
			// Empty blocks (txCount==0) with nil/empty data → not a miss.
			if txCount == 0 {
				return nil
			}
		}
	}

	// Fallback: old freezer format (ancient/senders).
	if e.senderTable != nil && blockNum < e.senderTable.Items() {
		senderData, err := e.senderTable.Retrieve(blockNum)
		if err == nil {
			senderCount := len(senderData) / 20
			if senderCount == txCount && txCount > 0 {
				senders := make([]types.Address, senderCount)
				for i := 0; i < senderCount; i++ {
					copy(senders[i][:], senderData[i*20:(i+1)*20])
				}
				return senders
			}
			if txCount == 0 {
				return nil
			}
		}
	}

	if (e.senderStore != nil || e.senderTable != nil) && txCount > 0 {
		e.senderMisses++
	}
	return nil
}

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
func (e *Executor) writeOutputs(blockNum uint64, result *BlockResult, writer *state.BufferedPlainStateWriter, witness *WitnessStateReader, dbTx kv.Tx, blockWipes []types.Address) error {
	b := e.outBatcher

	// 1. Receipts/senders: not written. Both tables are being deprecated;
	// receipts can be re-derived by replaying transactions when needed,
	// and senders are produced by the dedicated sender-recovery stage on
	// demand. We keep the function parameter shape so callers don't need
	// to change.

	// 2. Unified changesets — per-entry carries both old and new values,
	// so forward replay (rebuild-state) and backward unwind (reorg) share
	// a single data source and neither needs the EVM. The legacy leaves
	// journal is no longer written; block 0 uses a dedicated genesis
	// encoder that walks the initial PlainState and emits the V2 blobs
	// with oldLen=0.
	var accCSBytes, stoCSBytes []byte
	if blockNum == 0 {
		var err error
		accCSBytes, err = EncodeGenesisAccounts(newGenesisAccountIterator(dbTx))
		if err != nil {
			return fmt.Errorf("encode genesis accounts: %w", err)
		}
		stoCSBytes, err = EncodeGenesisStorages(newGenesisStorageIterator(dbTx))
		if err != nil {
			return fmt.Errorf("encode genesis storages: %w", err)
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

			// Buffered reader sees the post-block state (write buffer
			// shadowing MDBX). new-value callbacks read through it.
			bufReader := state.NewBufferedPlainStateReader(e.stateBuf, dbTx)

			accCSBytes = EncodeAccountChanges(accCS, func(addr types.Address) []byte {
				a, err := bufReader.ReadAccountData(addr)
				if err != nil || a == nil {
					return nil
				}
				return a.MarshalV2()
			})
			stoCSBytes = EncodeStorageChanges(stoCS, func(addr types.Address, slot types.Hash) []byte {
				v, err := bufReader.ReadAccountStorage(addr, 0, &slot)
				if err != nil {
					return nil
				}
				return v
			})
		}
	}
	if !e.cfg.LeavesOnly {
		if err := b.addEntry(freezer.TableAccountChanges, "c", accCSBytes); err != nil {
			return fmt.Errorf("account changes: %w", err)
		}
		if err := b.addEntry(freezer.TableStorageChanges, "c", stoCSBytes); err != nil {
			return fmt.Errorf("storage changes: %w", err)
		}
	}
	_ = blockWipes // Retained for signature parity; wipes are now implicit in
	// storcs entries (each wiped slot has oldLen>0, newLen=0).

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

// dumpGasMismatch is a diagnostic invoked when a block's total gas used
// disagrees with the geth header. It loads geth's stored receipts for
// the same block, walks them in tx order alongside the freshly-computed
// N42 receipts, and prints the first transaction whose per-tx gas
// (CumulativeGasUsed delta) differs. Best-effort: logs nothing fatal if
// geth receipts can't be fetched.
func (e *Executor) dumpGasMismatch(blockNum uint64, header *block.Header, body *GethBodyResult, result *BlockResult) {
	if e.freezer == nil {
		return
	}
	rawReceipts, err := e.freezer.Ancient(freezer.TableReceipts, blockNum)
	if err != nil {
		log.Warn("Gas-mismatch dump: cannot read geth receipts", "block", blockNum, "err", err)
		return
	}
	gethReceipts, err := DecodeGethReceipts(rawReceipts)
	if err != nil {
		log.Warn("Gas-mismatch dump: cannot decode geth receipts", "block", blockNum, "err", err)
		return
	}

	n := len(result.Receipts)
	if len(gethReceipts) < n {
		n = len(gethReceipts)
	}
	var prevN42, prevGeth uint64
	totalDiffs := 0
	totalDiffSum := int64(0)
	for i := 0; i < n; i++ {
		n42Cum := result.Receipts[i].CumulativeGasUsed
		gethCum := gethReceipts[i].CumulativeGasUsed
		n42Tx := n42Cum - prevN42
		gethTx := gethCum - prevGeth
		prevN42, prevGeth = n42Cum, gethCum
		if n42Tx != gethTx {
			totalDiffs++
			d := int64(gethTx) - int64(n42Tx)
			totalDiffSum += d
			var txHash types.Hash
			if i < len(body.Transactions) {
				txHash = body.Transactions[i].Hash()
			}
			log.Error("Gas-mismatch tx",
				"block", blockNum,
				"txIndex", i,
				"txHash", txHash.Hex(),
				"n42Gas", n42Tx,
				"gethGas", gethTx,
				"diff", d,
				"txType", result.Receipts[i].Type,
				"status", result.Receipts[i].Status,
			)
		}
	}
	log.Error("Gas-mismatch totals",
		"block", blockNum,
		"diffs", totalDiffs,
		"sumDiff", totalDiffSum,
	)
	log.Error("Gas-mismatch dump: per-tx gas matches in shared range; block totals still differ",
		"block", blockNum,
		"n42Receipts", len(result.Receipts),
		"gethReceipts", len(gethReceipts),
		"n42Gas", result.GasUsed,
		"gethGas", header.GasUsed,
	)
}

