// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// witness_replay_pipeline.go — orchestrator for parallel witness-driven
// block replay. The pipeline:
//
//   [Reader goroutine] → [block channel cap=256]
//                       ↓
//                  [Worker pool ×N]   (N=32 default; each holds own RoTx)
//                       ↓
//                  [result channel cap=256]
//                       ↓
//                  [Aggregator goroutine]
//                  (reorders by block num, sequential cdat append via
//                   outputBatcher; accumulates flushFullBatches)
//
// State application is intentionally OUT of band: after the pipeline
// finishes producing acctcs/storcs cdat, callers run rebuild-state on
// the same datadir to populate MDBX PlainState. Splitting replay
// (parallel, CPU-bound) from state apply (sequential, MDBX-bound)
// keeps both phases simple and lets you re-apply the cdat without
// re-replaying the witness.

package ethel

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

// WitnessReplayConfig configures a parallel witness replay run.
type WitnessReplayConfig struct {
	// Input freezer (read-only): headers + bodies tables.
	HeadersBodiesPath string
	// Input freezer (read-only): block_witness table. May be the same
	// directory as HeadersBodiesPath if witness lives there.
	WitnessPath string
	// Output freezer (writable): acctcs / storcs / receipts / witness
	// tables. Typically equals the datadir's chain/freezer dir.
	OutputPath string
	// Datadir holding MDBX with the Code table. Workers each open a
	// short-lived RoTx against this DB.
	Datadir string
	// SendersPath is an optional pre-computed senders freezer dir
	// (table=senders). If empty, ecrecover runs per tx.
	SendersPath string

	StartBlock uint64
	EndBlock   uint64 // exclusive; 0 means "all available witness items"
	Workers    int    // 0 → 32

	// NoOutput disables cdat writes. Workers still replay + verify
	// gas, but no acctcs/storcs/receipts/witness cdat is written.
	// Useful for throughput smoke tests against an arbitrary block
	// range without having to pad the output freezer to startBlock.
	NoOutput bool

	// SkipVerify disables gas verification. Use when measuring raw
	// pipeline throughput against a witness that may have been
	// recorded by a different ProcessBlock version (state-read order
	// drift produces gas mismatches that aren't a framework bug).
	SkipVerify bool

	// ContinueOnError lets the pipeline keep replaying past a per-block
	// failure (logged + counted). Throughput measurement against a
	// possibly-stale witness needs this; production runs should leave
	// it false so any divergence halts immediately.
	ContinueOnError bool

	// WriteWitness writes the witness.cdat table to the output freezer
	// alongside receipts/acctcs/storcs. Off by default — typical replay
	// reads existing witness from --input-witness, so re-emitting it
	// duplicates the input. Set when generating a fresh witness archive.
	WriteWitness bool

	// ReceiptsFromPath overrides the dir used to recompute Header.Bloom
	// when reading from N42 columnar headers (which strip bloom). Only
	// used when --input-headers-bodies points at a columnar archive.
	// Default: same as HeadersBodiesPath. Override when those receipts
	// are incomplete and you have the full geth ancient receipts handy.
	ReceiptsFromPath string

	ChainCfg *params.ChainConfig
	Engine   consensus.Engine
}

// witnessAggregateState owns the outputBatcher and is the only thing
// that writes to it (sequentially, in block order).
type witnessAggregateState struct {
	batcher      *outputBatcher
	pending      map[uint64]WitnessResult
	next         uint64
	end          uint64
	writeWitness bool
}

// RunWitnessReplay drives the parallel replay end-to-end. Returns
// nil on success after every block in [start, end) is replayed,
// verified, and persisted to cdat. Caller is expected to run
// rebuild-state on the same datadir afterward to populate PlainState.
func RunWitnessReplay(ctx context.Context, cfg WitnessReplayConfig, codeDB kv.RoDB) error {
	if cfg.ChainCfg == nil {
		return fmt.Errorf("witnessreplay: ChainCfg is nil")
	}
	if cfg.Engine == nil {
		return fmt.Errorf("witnessreplay: Engine is nil")
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 32
	}

	// 1. Open input source for headers + bodies. Auto-detects N42's
	// columnar HeaderCompactReader format when headers.cidx is present;
	// otherwise falls back to geth ancient (raw RLP per block).
	hbSource, err := openHeadersBodiesSource(cfg.HeadersBodiesPath, cfg.ReceiptsFromPath)
	if err != nil {
		return fmt.Errorf("open headers/bodies source: %w", err)
	}
	defer hbSource.close()
	switch hbSource.(type) {
	case *n42CompactSource:
		log.Info("Headers/bodies source", "format", "n42-columnar", "path", cfg.HeadersBodiesPath, "max_block", hbSource.maxBlock())
	default:
		log.Info("Headers/bodies source", "format", "geth-ancient", "path", cfg.HeadersBodiesPath, "frozen", hbSource.maxBlock())
	}

	witnessTbl, err := freezer.NewFreezerTableCompressedReadOnly(
		cfg.WitnessPath, freezer.TableBlockWitness, "c")
	if err != nil {
		return fmt.Errorf("open witness table at %s: %w", cfg.WitnessPath, err)
	}
	defer witnessTbl.Close()
	witnessTbl.ForceBatchSize(freezer.BatchSize)

	// Optional pre-computed senders (avoids ecrecover, the dominant
	// CPU cost on tx-dense blocks per pprof — single libsecp256k1 mutex
	// serializes 32 workers behind cgocall, killing parallelism).
	var sendersTbl *freezer.FreezerTable
	if cfg.SendersPath != "" {
		// Senders cdat in N42 is compressed (.cdat batches via zstd) —
		// use the compressed read-only opener. The previous "raw" open
		// silently produced an empty/invalid table that fell back to
		// ecrecover unbeknownst to the user.
		sendersTbl, err = freezer.NewFreezerTableCompressedReadOnly(cfg.SendersPath, freezer.TableSenders, "c")
		if err != nil {
			log.Warn("WitnessReplay: senders table not opened, ecrecover will be used", "err", err)
		} else {
			sendersTbl.ForceBatchSize(freezer.BatchSize)
			defer sendersTbl.Close()
			log.Info("Senders index loaded", "entries", sendersTbl.Items())
		}
	}

	// 2. Determine end block.
	end := cfg.EndBlock
	witItems := witnessTbl.Items()
	if end == 0 || end > witItems {
		end = witItems
	}
	if cfg.StartBlock >= end {
		return fmt.Errorf("witnessreplay: start %d >= end %d", cfg.StartBlock, end)
	}

	// 3. Open output freezer (for cdat writes), unless caller opted out.
	var batcher *outputBatcher
	if !cfg.NoOutput {
		outF, errOut := freezer.New(cfg.OutputPath, 0)
		if errOut != nil {
			return fmt.Errorf("open output freezer: %w", errOut)
		}
		defer outF.Close()

		batcher, err = newOutputBatcher(outF)
		if err != nil {
			return fmt.Errorf("new output batcher: %w", err)
		}
		if err := batcher.alignOnResume(cfg.StartBlock, false); err != nil {
			return fmt.Errorf("align on resume: %w", err)
		}
	}

	// 4. Wire up channels and worker pool.
	// Large channel buffers (8192) amortise the runtime mutex per
	// send/receive across many ops — profile showed runtime.lock2 +
	// stdcall2 ~21% under cap=256 with 32 workers. Bigger buffers let
	// the reader run ahead and workers pull in bursts without blocking.
	const chanCap = 8192
	blockCh := make(chan WitnessJob, chanCap)
	resultCh := make(chan WitnessResult, chanCap)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < cfg.Workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runWitnessWorker(ctx, id, blockCh, resultCh, codeDB, cfg.ChainCfg, cfg.Engine,
				ReplayMode{SkipVerify: cfg.SkipVerify, NoOutput: cfg.NoOutput})
		}(i)
	}

	// 5a. Genesis (block 0): handled inline before the worker pool runs
	// so we don't need to thread pre-encoded bytes through every worker.
	// The user's chaindata MDBX is post-replay so we can't iterate it
	// for genesis state; use the embedded mainnet alloc JSON via memdb.
	feedStart := cfg.StartBlock
	if cfg.StartBlock == 0 {
		acctcsBytes, storcsBytes, err := encodeEthMainnetGenesis()
		if err != nil {
			return fmt.Errorf("genesis encode: %w", err)
		}
		if batcher != nil {
			if err := batcher.addEntry(freezer.TableReceipts, "c", EncodeReceiptsCompact(nil)); err != nil {
				return fmt.Errorf("addEntry receipts block 0: %w", err)
			}
			if err := batcher.addEntry(freezer.TableAccountChanges, "c", acctcsBytes); err != nil {
				return fmt.Errorf("addEntry acctcs block 0: %w", err)
			}
			if err := batcher.addEntry(freezer.TableStorageChanges, "c", storcsBytes); err != nil {
				return fmt.Errorf("addEntry storcs block 0: %w", err)
			}
			if cfg.WriteWitness {
				if err := batcher.addEntry(freezer.TableBlockWitness, "c", nil); err != nil {
					return fmt.Errorf("addEntry witness block 0: %w", err)
				}
			}
		}
		log.Info("Genesis encoded",
			"acctcs", len(acctcsBytes),
			"storcs", len(storcsBytes))
		feedStart = 1
	}

	// 5b. Reader goroutine (blocks 1..end, or feedStart..end).
	readerErr := make(chan error, 1)
	go func() {
		defer close(blockCh)
		readerErr <- feedBlocks(ctx, hbSource, witnessTbl, sendersTbl,
			cfg.ChainCfg, feedStart, end, blockCh)
	}()

	// 6. Aggregator (this goroutine).
	agg := &witnessAggregateState{
		batcher:      batcher,
		pending:      make(map[uint64]WitnessResult),
		next:         feedStart,
		end:          end,
		writeWitness: cfg.WriteWitness,
	}
	target := end - cfg.StartBlock
	log.Info("Replay started",
		"range", fmt.Sprintf("%d-%d", cfg.StartBlock, end),
		"blocks", target,
		"workers", cfg.Workers,
		"output", outputDescription(cfg))

	t0 := time.Now()
	lastLog := t0
	var (
		failed       uint64
		totalGas     uint64
		totalTxs     uint64
		windowGas    uint64
		windowTxs    uint64
		windowBlocks uint64
	)

	go func() {
		// Wait for workers, then close result channel so aggregator can exit.
		wg.Wait()
		close(resultCh)
	}()

	for r := range resultCh {
		if r.Err != nil {
			if !cfg.ContinueOnError {
				cancel()
				return fmt.Errorf("witnessreplay: %w", r.Err)
			}
			failed++
			// Treat as empty so aggregator's sequential counter advances.
			r = WitnessResult{BlockNum: r.BlockNum}
		}
		totalGas += r.GasUsed
		totalTxs += uint64(r.TxCount)
		windowGas += r.GasUsed
		windowTxs += uint64(r.TxCount)
		windowBlocks++
		if err := agg.absorb(r); err != nil {
			cancel()
			return err
		}
		if d := time.Since(lastLog); d > 10*time.Second {
			elapsed := time.Since(t0)
			done := agg.next - cfg.StartBlock
			eta := time.Duration(0)
			if done > 0 && done < target {
				eta = time.Duration(float64(elapsed) * float64(target-done) / float64(done)).Truncate(time.Second)
			}
			log.Info("Replay progress",
				"head", agg.next-1,
				"progress", fmt.Sprintf("%5.2f%%", 100*float64(done)/float64(target)),
				"blk/s", fmt.Sprintf("%6.0f", float64(windowBlocks)/d.Seconds()),
				"mgas/s", fmt.Sprintf("%6.1f", float64(windowGas)/d.Seconds()/1e6),
				"tx/s", fmt.Sprintf("%6.0f", float64(windowTxs)/d.Seconds()),
				"elapsed", elapsed.Truncate(time.Second),
				"eta", eta)
			lastLog = time.Now()
			windowGas, windowTxs, windowBlocks = 0, 0, 0
		}
	}

	if err := <-readerErr; err != nil {
		return fmt.Errorf("reader: %w", err)
	}

	// 7. Final flush.
	if agg.batcher != nil {
		if err := agg.batcher.flushAll(); err != nil {
			return fmt.Errorf("final flushAll: %w", err)
		}
		if err := agg.batcher.sync(); err != nil {
			return fmt.Errorf("final sync: %w", err)
		}
	}
	elapsed := time.Since(t0)
	done := agg.next - cfg.StartBlock
	log.Info("Replay complete",
		"head", agg.next-1,
		"blocks", done,
		"txs", totalTxs,
		"gas", totalGas,
		"failed", failed,
		"blk/s", fmt.Sprintf("%.0f", float64(done)/elapsed.Seconds()),
		"mgas/s", fmt.Sprintf("%.1f", float64(totalGas)/elapsed.Seconds()/1e6),
		"elapsed", elapsed.Truncate(time.Second))
	return nil
}

func outputDescription(cfg WitnessReplayConfig) string {
	if cfg.NoOutput {
		return "(none, smoke run)"
	}
	return cfg.OutputPath
}

// absorb takes a worker result, places it in the pending map, then
// drains pending entries in block order, writing each to the
// outputBatcher (skipped when batcher is nil — NoOutput mode).
func (a *witnessAggregateState) absorb(r WitnessResult) error {
	a.pending[r.BlockNum] = r
	for {
		res, ok := a.pending[a.next]
		if !ok {
			return nil
		}
		if a.batcher != nil {
			if err := a.batcher.addEntry(freezer.TableReceipts, "c", res.ReceiptBytes); err != nil {
				return fmt.Errorf("addEntry receipts block %d: %w", res.BlockNum, err)
			}
			if err := a.batcher.addEntry(freezer.TableAccountChanges, "c", res.AcctCSBytes); err != nil {
				return fmt.Errorf("addEntry acctcs block %d: %w", res.BlockNum, err)
			}
			if err := a.batcher.addEntry(freezer.TableStorageChanges, "c", res.StoCSBytes); err != nil {
				return fmt.Errorf("addEntry storcs block %d: %w", res.BlockNum, err)
			}
			// Witness output is opt-in (WriteWitness=true). When on, the
			// entry must always be emitted — even if empty for a txless
			// block — so the witness table stays 64-batch-aligned with
			// receipts/acctcs/storcs.
			if a.writeWitness {
				if err := a.batcher.addEntry(freezer.TableBlockWitness, "c", res.WitnessBytes); err != nil {
					return fmt.Errorf("addEntry witness block %d: %w", res.BlockNum, err)
				}
			}
			if _, err := a.batcher.flushFullBatches(); err != nil {
				return fmt.Errorf("flushFullBatches block %d: %w", res.BlockNum, err)
			}
		}
		delete(a.pending, a.next)
		a.next++
		if a.next >= a.end {
			return nil
		}
	}
}

// feedBlocks streams (header, body, witness, senders) tuples into
// blockCh in block order. Decoding happens on the reader goroutine so
// workers don't waste CPU on sequential I/O.
func feedBlocks(
	ctx context.Context,
	hbSource headersBodiesSource,
	witnessTbl *freezer.FreezerTable,
	sendersTbl *freezer.FreezerTable,
	chainCfg *params.ChainConfig,
	start, end uint64,
	out chan<- WitnessJob,
) error {
	// Sliding window of the last blockHashWindowSize canonical hashes.
	// Populated as we read each block; passed by snapshot into each
	// WitnessJob so workers' BLOCKHASH closures can resolve ancestors
	// without touching the source (which is not goroutine-safe for
	// the n42 columnar reader).
	recent := make([]types.Hash, 0, blockHashWindowSize)
	// Resume case: prewarm the window. The n42 columnar reader strips
	// ParentHash, and rebuilding the chain requires sequential reads
	// from genesis. Walk all ancestors so block `start` sees a fully-
	// populated BLOCKHASH window.
	if start > 0 {
		t0 := time.Now()
		for n := uint64(0); n < start; n++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			hdr, err := hbSource.header(n)
			if err != nil {
				return fmt.Errorf("prewarm header %d: %w", n, err)
			}
			recent = append(recent, hdr.Hash())
			if len(recent) > blockHashWindowSize {
				recent = recent[1:]
			}
		}
		log.Info("BLOCKHASH window prewarmed",
			"start", start,
			"hashes", len(recent),
			"elapsed", time.Since(t0).Truncate(time.Millisecond))
	}
	for blockNum := start; blockNum < end; blockNum++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		hdr, err := hbSource.header(blockNum)
		if err != nil {
			return fmt.Errorf("read header %d: %w", blockNum, err)
		}
		body, err := hbSource.body(blockNum)
		if err != nil {
			return fmt.Errorf("read body %d: %w", blockNum, err)
		}

		witnessData, err := witnessTbl.Retrieve(blockNum)
		if err != nil {
			return fmt.Errorf("read witness %d: %w", blockNum, err)
		}

		var senders []types.Address
		if sendersTbl != nil && blockNum < sendersTbl.Items() {
			if data, err := sendersTbl.Retrieve(blockNum); err == nil {
				if n := len(data) / 20; n == len(body.Transactions) {
					senders = make([]types.Address, n)
					for i := range senders {
						copy(senders[i][:], data[i*20:(i+1)*20])
					}
				}
			}
		}
		if senders == nil && len(body.Transactions) > 0 {
			signer := transaction.MakeSigner(chainCfg, hdr.Number.ToBig())
			senders = make([]types.Address, len(body.Transactions))
			for i, txn := range body.Transactions {
				if s, err := transaction.Sender(signer, txn); err == nil {
					senders[i] = s
				}
			}
		}

		// BlockHashFn must match recording-side semantics: BLOCKHASH(n)
		// returns the canonical hash of block n if 1 ≤ blockNum-n ≤ 256,
		// else 0. Pull from the headers freezer (immutable, concurrent-
		// safe). Without this, contracts using BLOCKHASH took different
		// EVM branches in replay vs recording → gas mismatch / "Gas pool
		// exhausted" — root cause of the 11% block-failure rate observed
		// on 12M-12.2M.
		blockHashFn := makeBlockHashFn(blockNum, recent)

		job := WitnessJob{
			BlockNum:    blockNum,
			Header:      hdr,
			Body:        body,
			Witness:     witnessData,
			Senders:     senders,
			BlockHashFn: blockHashFn,
		}

		// Slide window forward AFTER the job is dispatched: the job
		// is for `blockNum`, and its BLOCKHASH window covers
		// [blockNum-256, blockNum-1] which is exactly `recent` here.
		recent = append(recent, hdr.Hash())
		if len(recent) > blockHashWindowSize {
			recent = recent[1:]
		}
		select {
		case out <- job:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// _ block.Header silence-import if needed
var _ = block.Header{}
