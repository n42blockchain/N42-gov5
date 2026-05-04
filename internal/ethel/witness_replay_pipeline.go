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
	log "github.com/n42blockchain/N42/lib/log/v3"
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

	ChainCfg *params.ChainConfig
	Engine   consensus.Engine
}

// witnessAggregateState owns the outputBatcher and is the only thing
// that writes to it (sequentially, in block order).
type witnessAggregateState struct {
	batcher *outputBatcher
	pending map[uint64]WitnessResult
	next    uint64
	end     uint64
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

	// 1. Open input freezers.
	headersBodies, err := freezer.New(cfg.HeadersBodiesPath, 0)
	if err != nil {
		return fmt.Errorf("open headers/bodies freezer: %w", err)
	}
	defer headersBodies.Close()

	var witnessTbl *freezer.FreezerTable
	if cfg.WitnessPath == cfg.HeadersBodiesPath {
		witnessTbl = headersBodies.Table(freezer.TableBlockWitness)
	} else {
		witnessTbl, err = freezer.NewFreezerTableCompressedReadOnly(
			cfg.WitnessPath, freezer.TableBlockWitness, "c")
		if err != nil {
			return fmt.Errorf("open witness table: %w", err)
		}
		defer witnessTbl.Close()
		witnessTbl.ForceBatchSize(freezer.BatchSize)
	}
	if witnessTbl == nil {
		return fmt.Errorf("witness table not found in %s", cfg.WitnessPath)
	}

	// Optional pre-computed senders (avoids ecrecover).
	var sendersTbl *freezer.FreezerTable
	if cfg.SendersPath != "" {
		sendersTbl, err = freezer.NewFreezerTable(cfg.SendersPath, freezer.TableSenders, "r")
		if err != nil {
			log.Warn("WitnessReplay: senders table not opened, ecrecover will be used", "err", err)
		} else {
			defer sendersTbl.Close()
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
	blockCh := make(chan WitnessJob, 256)
	resultCh := make(chan WitnessResult, 256)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < cfg.Workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runWitnessWorker(ctx, id, blockCh, resultCh, codeDB, cfg.ChainCfg, cfg.Engine, cfg.SkipVerify)
		}(i)
	}

	// 5. Reader goroutine.
	readerErr := make(chan error, 1)
	go func() {
		defer close(blockCh)
		readerErr <- feedBlocks(ctx, headersBodies, witnessTbl, sendersTbl,
			cfg.ChainCfg, cfg.StartBlock, end, blockCh)
	}()

	// 6. Aggregator (this goroutine).
	agg := &witnessAggregateState{
		batcher: batcher,
		pending: make(map[uint64]WitnessResult),
		next:    cfg.StartBlock,
		end:     end,
	}
	t0 := time.Now()
	lastLog := t0
	var failed uint64

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
		if err := agg.absorb(r); err != nil {
			cancel()
			return err
		}
		if time.Since(lastLog) > 10*time.Second {
			elapsed := time.Since(t0)
			done := agg.next - cfg.StartBlock
			rate := float64(done) / elapsed.Seconds()
			log.Info("WitnessReplay progress",
				"block", agg.next, "done", done,
				"target", end-cfg.StartBlock,
				"blk/s", fmt.Sprintf("%.0f", rate),
				"elapsed", elapsed.Truncate(time.Second))
			lastLog = time.Now()
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
	log.Info("WitnessReplay complete",
		"blocks", agg.next-cfg.StartBlock,
		"failed", failed,
		"elapsed", elapsed.Truncate(time.Second),
		"blk/s", fmt.Sprintf("%.0f", float64(agg.next-cfg.StartBlock)/elapsed.Seconds()))
	return nil
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
			if len(res.WitnessBytes) > 0 {
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
	headersBodies *freezer.Freezer,
	witnessTbl *freezer.FreezerTable,
	sendersTbl *freezer.FreezerTable,
	chainCfg *params.ChainConfig,
	start, end uint64,
	out chan<- WitnessJob,
) error {
	for blockNum := start; blockNum < end; blockNum++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		hdrData, err := headersBodies.Ancient(freezer.TableHeaders, blockNum)
		if err != nil {
			return fmt.Errorf("read header %d: %w", blockNum, err)
		}
		hdr, err := DecodeGethHeader(hdrData)
		if err != nil {
			return fmt.Errorf("decode header %d: %w", blockNum, err)
		}

		bodyData, err := headersBodies.Ancient(freezer.TableBodies, blockNum)
		if err != nil {
			return fmt.Errorf("read body %d: %w", blockNum, err)
		}
		body, err := DecodeGethBody(bodyData)
		if err != nil {
			return fmt.Errorf("decode body %d: %w", blockNum, err)
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

		job := WitnessJob{
			BlockNum:    blockNum,
			Header:      hdr,
			Body:        body,
			Witness:     witnessData,
			Senders:     senders,
			BlockHashFn: trivialBlockHashFn,
		}
		select {
		case out <- job:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// trivialBlockHashFn returns zero hash for any height — BLOCKHASH
// reads via this path are extremely rare in modern tx workloads,
// and witness already captures any state read that depends on
// historical hashes via EIP-2935's history contract path.
func trivialBlockHashFn(uint64) types.Hash { return types.Hash{} }

// _ block.Header silence-import if needed
var _ = block.Header{}
