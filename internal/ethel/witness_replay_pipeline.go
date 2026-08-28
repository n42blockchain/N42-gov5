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
	"errors"
	"fmt"
	"runtime/pprof"
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
	// Readers controls independent header/body/witness input feeders in
	// NoOutput mode. Each feeder owns its readers and processes whole compact
	// segments, so there are no shared decoder locks or duplicate body decodes.
	// 0 selects an automatic count capped at 6. Output mode stays sequential.
	Readers int

	// BodyDecoders bounds how many readers may expand a whole BODYC segment at
	// the same time. It is the input pipeline's real throughput knob: a segment
	// expansion is single-threaded, so this caps aggregate input rate, while the
	// rest of a reader's work (witness/senders retrieval, job assembly) stays
	// concurrent. 0 keeps the derived default, which is ceil(Workers/128) — that
	// formula was never measured and has a cliff at 128 workers (1 decoder) vs
	// 129 (2), so pin it explicitly when the input rate is what is under test.
	BodyDecoders int

	// SegmentShardCount/Index select an interleaved subset of compact BODYC
	// segments for an internal process child. Interleaving instead of assigning
	// contiguous block ranges keeps children balanced when historical eras have
	// radically different gas/transaction density. User-facing callers leave
	// count at zero/one and process every segment.
	SegmentShardCount int
	SegmentShardIndex int

	// InputHighWaterBytes/InputLowWaterBytes enable a byte-accounted input
	// reservoir in NoOutput mode. Producers fill until High, then wait for
	// completed workers to drain below Low before refilling. Zero disables the
	// reservoir and keeps the ordinary bounded channel.
	InputHighWaterBytes int64
	InputLowWaterBytes  int64

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

	// WriteReceipts opts in to emitting the receipts.cdat output table.
	// Off by default — witness-replay's primary outputs are witness +
	// acctcs + storcs; receipts are derived separately by the receipt-copy
	// subcommand which has its own CPU/I/O budget. The per-block receipts
	// root is still verified in-memory either way; this flag only governs
	// whether the persisted cdat is written.
	WriteReceipts bool

	ChainCfg *params.ChainConfig
	Engine   consensus.Engine

	// CodesFreezerDir, if set, points to a freezer dir containing
	// codes.cidx + codes.NNNN.cdat (produced by code-import2fz). The
	// reader uses it as the address-indexed bytecode source — works
	// from genesis and doesn't require a populated MDBX Code table.
	// When both this and codeDB are set, codes-freezer takes precedence.
	CodesFreezerDir string
}

// witnessAggregateState owns the outputBatcher and is the only thing
// that writes to it (sequentially, in block order).
type witnessAggregateState struct {
	batcher       *outputBatcher
	reorder       *reorderBuffer
	next          uint64
	end           uint64
	writeWitness  bool
	writeReceipts bool
	// out, when non-nil, receives results in strict block order; a separate
	// writer goroutine drains it and does the (sequential, I/O-bound) freezer
	// write, so the EVM workers + aggregator never block on disk ("CS = ordered
	// async write"). nil in NoOutput mode (absorb just advances the head).
	out chan<- WitnessResult
	ctx context.Context
}

// RunWitnessReplay drives the parallel replay end-to-end. Returns
// nil on success after every block in [start, end) is replayed,
// verified, and persisted to cdat. Caller is expected to run
// rebuild-state on the same datadir afterward to populate PlainState.
func RunWitnessReplay(ctx context.Context, cfg WitnessReplayConfig, codeDB kv.RoDB) error {
	if cfg.SegmentShardCount <= 0 {
		cfg.SegmentShardCount = 1
	}
	if cfg.SegmentShardIndex < 0 || cfg.SegmentShardIndex >= cfg.SegmentShardCount {
		return fmt.Errorf("invalid segment shard %d/%d", cfg.SegmentShardIndex, cfg.SegmentShardCount)
	}
	if cfg.SegmentShardCount > 1 && !cfg.NoOutput {
		return fmt.Errorf("segment-sharded replay requires NoOutput")
	}
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
	hbSource, err := openHeadersBodiesSource(cfg.HeadersBodiesPath)
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
		// witness-replay writes receipts in addition to per-block
		// changesets — they all need to share a head, so receipts is
		// in this list (unlike ethexec, which doesn't emit receipts
		// to the freezer at all).
		witnessReplayOutputTables := []string{
			freezer.TableAccountChanges,
			freezer.TableStorageChanges,
			freezer.TableWipes,
		}
		if cfg.WriteReceipts {
			witnessReplayOutputTables = append(witnessReplayOutputTables, freezer.TableReceipts)
		}
		// Include witness in the align list only when re-emitting it.
		// Otherwise resumes hit "gap too large" on the witness table
		// (which the run never wrote) and refuse to start.
		if cfg.WriteWitness {
			witnessReplayOutputTables = append(witnessReplayOutputTables, freezer.TableBlockWitness)
		}
		if err := batcher.alignOnResume(witnessReplayOutputTables, cfg.StartBlock, false); err != nil {
			return fmt.Errorf("align on resume: %w", err)
		}
	}

	// 4. Wire up channels and worker pool.
	//
	// Channel cap scales with the worker count, between two failure modes.
	//
	// Too large: the aggregator emits in block order, so one slow 10M-gas DeFi
	// block at the head lets every other worker pile results into `pending`.
	// The original 8192 let the heap balloon past 15 GB and pushed Go GC into
	// multi-second STW, feeding back into per-worker slowdown and eventually a
	// stall that looked like a deadlock. A flat 256 fixed that.
	//
	// Too small: 256 is only a few rounds of work for a large fleet, and the
	// reader is a SINGLE goroutine that must decompress a header, a bodyc
	// segment and a witness entry per block. On a 128-core host with ~104
	// workers that queue drains faster than one reader can refill it, and the
	// machine idles: measured 99.4% idle CPU while the run was nominally at
	// 32.7 Ggas/s, with vmstat's runnable count oscillating 54 -> 2 -> 1 as the
	// fleet alternated between a fed burst and collective starvation.
	//
	// Four rounds per worker keeps the reader far enough ahead to absorb its own
	// segment-decode spikes, while the ceiling preserves the heap bound that
	// motivated 256 in the first place: pending is bounded by this cap, not by
	// the worker count.
	chanCap := cfg.Workers * 4
	if chanCap < 256 {
		chanCap = 256
	}
	if chanCap > 2048 {
		chanCap = 2048
	}
	blockCh := make(chan WitnessJob, chanCap)
	resultCh := make(chan WitnessResult, chanCap)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Optional verification reservoir. The relay owns an elastic FIFO whose
	// retained payload is bounded by byte reservations, not by a guessed block
	// count. This lets a BODYC producer finish handing off an 8192-block decode
	// and immediately start the next segment while workers freely pull blocks.
	feedCh := blockCh
	var reservoir *replayInputReservoir
	var relayDone chan struct{}
	if cfg.NoOutput && cfg.InputHighWaterBytes > 0 {
		low := cfg.InputLowWaterBytes
		if low <= 0 || low >= cfg.InputHighWaterBytes {
			low = cfg.InputHighWaterBytes / 2
		}
		reservoir = newReplayInputReservoir(low, cfg.InputHighWaterBytes)
		feedCh = make(chan WitnessJob, 256)
		blockCh = make(chan WitnessJob)
		relayDone = make(chan struct{})
		go func() {
			defer close(relayDone)
			relayWitnessJobs(ctx, feedCh, blockCh)
		}()
		log.Info("Input reservoir enabled",
			"low_gb", float64(low)/(1<<30),
			"high_gb", float64(cfg.InputHighWaterBytes)/(1<<30))
	}

	// Optional address-indexed codes source. When present, workers
	// look up bytecode by address (binary-search the cidx) without
	// needing the MDBX Code table — important when starting from
	// genesis where MDBX state is empty.
	var codesReader *CodesFreezerReader
	if cfg.CodesFreezerDir != "" {
		var err error
		codesReader, err = NewCodesFreezerReader(cfg.CodesFreezerDir)
		if err != nil {
			return fmt.Errorf("open codes-freezer at %s: %w", cfg.CodesFreezerDir, err)
		}
		defer codesReader.Close()
		log.Info("Codes freezer attached", "dir", cfg.CodesFreezerDir, "items", codesReader.Items())
	}
	if codesReader == nil && codeDB == nil {
		return fmt.Errorf("witnessreplay: at least one of --codes-freezer or --datadir (with populated Code table) is required")
	}

	var wg sync.WaitGroup
	for i := 0; i < cfg.Workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runWitnessWorker(ctx, id, blockCh, resultCh, codeDB, codesReader, cfg.ChainCfg, cfg.Engine,
				ReplayMode{SkipVerify: cfg.SkipVerify, NoOutput: cfg.NoOutput})
		}(i)
	}

	// 5a. Genesis (block 0): handled inline before the worker pool runs
	// so we don't need to thread pre-encoded bytes through every worker.
	// The user's chaindata MDBX is post-replay so we can't iterate it
	// for genesis state; use the embedded mainnet alloc JSON via memdb.
	feedStart := cfg.StartBlock
	ownsGenesis := cfg.StartBlock == 0 && segmentShardOwns(0, cfg.SegmentShardCount, cfg.SegmentShardIndex)
	if ownsGenesis {
		acctcsBytes, storcsBytes, err := encodeEthMainnetGenesis()
		if err != nil {
			return fmt.Errorf("genesis encode: %w", err)
		}
		if batcher != nil {
			if cfg.WriteReceipts {
				if err := batcher.addEntry(freezer.TableReceipts, "c", EncodeReceiptsCompact(nil)); err != nil {
					return fmt.Errorf("addEntry receipts block 0: %w", err)
				}
			}
			if err := batcher.addEntry(freezer.TableAccountChanges, "c", acctcsBytes); err != nil {
				return fmt.Errorf("addEntry acctcs block 0: %w", err)
			}
			if err := batcher.addEntry(freezer.TableStorageChanges, "c", storcsBytes); err != nil {
				return fmt.Errorf("addEntry storcs block 0: %w", err)
			}
			if err := batcher.addEntry(freezer.TableWipes, "c", nil); err != nil {
				return fmt.Errorf("addEntry wipes block 0: %w", err)
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
		defer close(feedCh)
		if cfg.NoOutput && cfg.Workers > 1 {
			readerErr <- feedBlocksParallel(ctx, cfg, feedStart, end, feedCh, reservoir)
			return
		}
		readerErr <- feedBlocks(ctx, hbSource, witnessTbl, sendersTbl,
			cfg.ChainCfg, feedStart, end, feedCh)
	}()

	// 6. Aggregator (this goroutine).
	// Reorder window ≥ the max out-of-order spread (reader feeds chanCap ahead +
	// Workers in flight); generous margin so the overflow map stays empty.
	agg := &witnessAggregateState{
		batcher:       batcher,
		next:          feedStart,
		end:           end,
		writeWitness:  cfg.WriteWitness,
		writeReceipts: cfg.WriteReceipts,
		ctx:           ctx,
	}
	if !cfg.NoOutput {
		agg.reorder = newReorderBuffer(chanCap + cfg.Workers + 256)
	}
	// Async CS writer: the freezer write is sequential + I/O-bound; running it in
	// its own goroutine (fed in strict block order by absorb) keeps the EVM
	// workers + aggregator off the disk path so they stay CPU-bound. nil-batcher
	// (NoOutput) skips it entirely.
	var writerCh chan WitnessResult
	writerDone := make(chan error, 1)
	if batcher != nil {
		writerCh = make(chan WitnessResult, chanCap)
		agg.out = writerCh
		go func() {
			var werr error
			for res := range writerCh {
				if werr == nil {
					if err := agg.writeOne(res); err != nil {
						werr = err
						cancel() // unblock absorb's select + stop workers
					}
				}
				// keep draining post-error so absorb never blocks on a full chan
			}
			writerDone <- werr
		}()
	} else {
		writerDone <- nil
	}
	target := segmentShardBlockCount(cfg.StartBlock, end, cfg.SegmentShardCount, cfg.SegmentShardIndex)
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
		completed    uint64
		highest      uint64
	)
	if ownsGenesis {
		completed = 1
	}
	if feedStart > 0 {
		highest = feedStart - 1
	}

	go func() {
		// Wait for workers, then close result channel so aggregator can exit.
		wg.Wait()
		close(resultCh)
	}()

	var loopErr error
	for r := range resultCh {
		if r.Err != nil {
			if !cfg.ContinueOnError {
				loopErr = fmt.Errorf("witnessreplay: %w", r.Err)
				cancel()
				break
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
		if cfg.NoOutput {
			completed++
			if r.BlockNum > highest {
				highest = r.BlockNum
			}
		} else {
			if err := agg.absorb(r); err != nil {
				loopErr = err
				cancel()
				break
			}
			completed = agg.next - cfg.StartBlock
			highest = agg.next - 1
		}
		if d := time.Since(lastLog); d > 10*time.Second {
			elapsed := time.Since(t0)
			done := completed
			eta := time.Duration(0)
			if done > 0 && done < target {
				eta = time.Duration(float64(elapsed) * float64(target-done) / float64(done)).Truncate(time.Second)
			}
			log.Info("Replay progress",
				"head", highest,
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

	// Stop the async CS writer and surface its error first (a write failure is
	// the real cause; absorb's ctx.Canceled is only the symptom).
	if batcher != nil {
		close(writerCh)
	}
	if werr := <-writerDone; werr != nil {
		return fmt.Errorf("cs writer: %w", werr)
	}
	if loopErr != nil {
		return loopErr
	}

	if err := <-readerErr; err != nil {
		return fmt.Errorf("reader: %w", err)
	}
	if relayDone != nil {
		<-relayDone
		current, peak, waits, waited := reservoir.stats()
		log.Info("Input reservoir complete",
			"current_gb", float64(current)/(1<<30),
			"peak_gb", float64(peak)/(1<<30),
			"refill_waits", waits, "waited", waited.Truncate(time.Millisecond))
	}
	if cfg.NoOutput && completed != target {
		return fmt.Errorf("witnessreplay: completed %d blocks, want %d", completed, target)
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
	done := completed
	log.Info("Replay complete",
		"head", highest,
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

// absorb takes a worker result, reorders it, and emits ready results in strict
// block order. With an async writer (a.out != nil) it hands each ordered result
// to the writer goroutine (which does the freezer I/O) so the aggregator never
// blocks on disk. In NoOutput mode (a.out nil) it just advances the head.
func (a *witnessAggregateState) absorb(r WitnessResult) error {
	a.reorder.put(r)
	for {
		res, ok := a.reorder.take(a.next)
		if !ok {
			return nil
		}
		if a.out != nil {
			select {
			case a.out <- res:
			case <-a.ctx.Done():
				return a.ctx.Err()
			}
		}
		a.next++
		if a.next >= a.end {
			return nil
		}
	}
}

// writeOne appends one block's outputs to the freezer in batch lockstep. Called
// only by the single writer goroutine, in strict block order, so the cdat tables
// stay 64-batch-aligned. (Same write sequence the aggregator used to do inline.)
func (a *witnessAggregateState) writeOne(res WitnessResult) error {
	if a.batcher == nil {
		return nil
	}
	if a.writeReceipts {
		if err := a.batcher.addEntry(freezer.TableReceipts, "c", res.ReceiptBytes); err != nil {
			return fmt.Errorf("addEntry receipts block %d: %w", res.BlockNum, err)
		}
	}
	if err := a.batcher.addEntry(freezer.TableAccountChanges, "c", res.AcctCSBytes); err != nil {
		return fmt.Errorf("addEntry acctcs block %d: %w", res.BlockNum, err)
	}
	if err := a.batcher.addEntry(freezer.TableStorageChanges, "c", res.StoCSBytes); err != nil {
		return fmt.Errorf("addEntry storcs block %d: %w", res.BlockNum, err)
	}
	if err := a.batcher.addEntry(freezer.TableWipes, "c", res.WipesBytes); err != nil {
		return fmt.Errorf("addEntry wipes block %d: %w", res.BlockNum, err)
	}
	if a.writeWitness {
		if err := a.batcher.addEntry(freezer.TableBlockWitness, "c", res.WitnessBytes); err != nil {
			return fmt.Errorf("addEntry witness block %d: %w", res.BlockNum, err)
		}
	}
	if _, err := a.batcher.flushFullBatches(); err != nil {
		return fmt.Errorf("flushFullBatches block %d: %w", res.BlockNum, err)
	}
	return nil
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
	// Each block's hash is read directly from the columnar segment
	// trailer (or from the geth header for ancient input), so the
	// window is populated for free as we walk; no prewarm needed.
	recent := make([]types.Hash, 0, blockHashWindowSize)
	// Resume case: seed the window by reading the prior 256 ancestor
	// hashes. Reads only the hash via source.header(n).Hash() — no
	// EVM execution, no body decode, no receipts.
	if start > blockHashWindowSize {
		t0 := time.Now()
		for n := start - blockHashWindowSize; n < start; n++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			hdr, err := hbSource.header(n)
			if err != nil {
				return fmt.Errorf("prewarm header %d: %w", n, err)
			}
			recent = append(recent, hdr.Hash())
		}
		log.Info("BLOCKHASH window prewarmed",
			"start", start,
			"hashes", len(recent),
			"elapsed", time.Since(t0).Truncate(time.Millisecond))
	} else if start > 0 {
		for n := uint64(0); n < start; n++ {
			hdr, err := hbSource.header(n)
			if err != nil {
				return fmt.Errorf("prewarm header %d: %w", n, err)
			}
			recent = append(recent, hdr.Hash())
		}
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
		var body *GethBodyResult
		if consuming, ok := hbSource.(consumingHeadersBodiesSource); ok {
			body, err = consuming.takeBody(blockNum)
		} else {
			body, err = hbSource.body(blockNum)
		}
		if err != nil {
			return fmt.Errorf("read body %d: %w", blockNum, err)
		}

		witnessData, err := witnessTbl.RetrieveSequential(blockNum)
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

type replayInputRange struct {
	start uint64
	end   uint64
}

type parallelReplayInput struct {
	hb      headersBodiesSource
	witness *freezer.FreezerTable
	senders *freezer.FreezerTable
}

func openParallelReplayInput(cfg WitnessReplayConfig, bodyDecodeGate chan struct{}) (*parallelReplayInput, error) {
	hb, err := openHeadersBodiesSource(cfg.HeadersBodiesPath)
	if err != nil {
		return nil, err
	}
	if compact, ok := hb.(*n42CompactSource); ok {
		compact.br.SetDecodeGate(bodyDecodeGate)
	}
	witness, err := freezer.NewFreezerTableCompressedReadOnly(
		cfg.WitnessPath, freezer.TableBlockWitness, "c")
	if err != nil {
		hb.close()
		return nil, err
	}
	witness.ForceBatchSize(freezer.BatchSize)

	var senders *freezer.FreezerTable
	if cfg.SendersPath != "" {
		senders, err = freezer.NewFreezerTableCompressedReadOnly(
			cfg.SendersPath, freezer.TableSenders, "c")
		if err != nil {
			_ = witness.Close()
			hb.close()
			return nil, err
		}
		senders.ForceBatchSize(freezer.BatchSize)
	}
	return &parallelReplayInput{hb: hb, witness: witness, senders: senders}, nil
}

func (in *parallelReplayInput) close() {
	if in.senders != nil {
		_ = in.senders.Close()
	}
	_ = in.witness.Close()
	in.hb.close()
}

// feedBlocksParallel is the verification-only input path. Compact body files
// are independently compressed in HeaderSegmentSize-block segments, so ranges
// are assigned at exactly those boundaries: each segment is decoded once by
// one reader. Every reader owns its header/body readers, zstd decoders and
// freezer tables. Whole BODYC decodes share a small adaptive gate: an
// 8192-block expansion is cheap in aggregate CPU but extremely allocation-heavy,
// so bounding only that stage avoids synchronized GC/memory-bandwidth bursts
// while the other readers keep assembling and dispatching decoded segments.
func feedBlocksParallel(
	ctx context.Context,
	cfg WitnessReplayConfig,
	start, end uint64,
	out chan<- WitnessJob,
	reservoir *replayInputReservoir,
) error {
	firstSegment := start / HeaderSegmentSize
	lastSegment := (end - 1) / HeaderSegmentSize
	segmentCount := 0
	for segment := firstSegment; segment <= lastSegment; segment++ {
		if segmentShardOwns(segment, cfg.SegmentShardCount, cfg.SegmentShardIndex) {
			segmentCount++
		}
	}
	if segmentCount == 0 {
		return nil
	}
	readers := cfg.Readers
	automatic := readers <= 0
	if automatic {
		readers = cfg.Workers / 16
		if readers < 2 {
			readers = 2
		}
		if readers > 6 {
			readers = 6
		}
	}
	if readers > segmentCount {
		readers = segmentCount
	}
	// Keep one range queued when auto-tuning. Starting a reader for every
	// segment maximizes simultaneous zstd allocation and memory-bandwidth
	// pressure, while leaving no follow-up work for a reader that finishes a
	// short boundary segment. On the 128-core replay host, 2-of-3 and 6-of-7
	// consistently beat all-segments-at-once.
	if automatic && segmentCount > 1 && readers >= segmentCount {
		readers = segmentCount - 1
	}
	if readers > cfg.Workers {
		readers = cfg.Workers
	}

	bodyDecoders := cfg.BodyDecoders
	if bodyDecoders <= 0 {
		bodyDecoders = bodyDecoderCount(cfg.Workers, readers)
	} else if bodyDecoders > readers {
		// More decoders than readers cannot be used: only a reader decodes.
		bodyDecoders = readers
	}
	log.Info("Parallel verification input",
		"readers", readers, "body_decoders", bodyDecoders,
		"segments", segmentCount, "unordered", true,
		"segment_shard", fmt.Sprintf("%d/%d", cfg.SegmentShardIndex, cfg.SegmentShardCount))
	ranges := make(chan replayInputRange, segmentCount)
	for segment := firstSegment; segment <= lastSegment; segment++ {
		if !segmentShardOwns(segment, cfg.SegmentShardCount, cfg.SegmentShardIndex) {
			continue
		}
		lo := segment * HeaderSegmentSize
		hi := lo + HeaderSegmentSize
		if lo < start {
			lo = start
		}
		if hi > end {
			hi = end
		}
		ranges <- replayInputRange{start: lo, end: hi}
	}
	close(ranges)

	feedCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// This is intentionally per process, not global. Process sharding is also
	// the NUMA/GC isolation boundary. One decoder feeds roughly one 128-worker
	// execution group; a full 254-worker process needs two, while two 127-worker
	// children each keep one. This avoids both single-decoder starvation and the
	// allocation storm caused by tying decode concurrency to reader count.
	bodyDecodeGate := make(chan struct{}, bodyDecoders)
	errs := make(chan error, readers)
	for i := 0; i < readers; i++ {
		go func(id int) {
			pprof.SetGoroutineLabels(pprof.WithLabels(ctx, pprof.Labels("phase", "read")))
			input, err := openParallelReplayInput(cfg, bodyDecodeGate)
			if err == nil {
				defer input.close()
				for r := range ranges {
					if err = feedBlockRange(feedCtx, input, cfg.ChainCfg, r, out, reservoir); err != nil {
						break
					}
				}
			}
			if err != nil {
				err = fmt.Errorf("parallel reader %d: %w", id, err)
				cancel()
			}
			errs <- err
		}(i)
	}

	var firstErr error
	for i := 0; i < readers; i++ {
		if err := <-errs; err != nil && !errors.Is(err, context.Canceled) && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return firstErr
}

func bodyDecoderCount(workers, readers int) int {
	decoders := (workers + 127) / 128
	if decoders < 1 {
		decoders = 1
	}
	if decoders > readers {
		decoders = readers
	}
	return decoders
}

func segmentShardOwns(segment uint64, count, index int) bool {
	if count <= 1 {
		return true
	}
	return int(segment%uint64(count)) == index
}

func segmentShardBlockCount(start, end uint64, count, index int) uint64 {
	if end <= start {
		return 0
	}
	if count <= 1 {
		return end - start
	}
	firstSegment := start / HeaderSegmentSize
	lastSegment := (end - 1) / HeaderSegmentSize
	var blocks uint64
	for segment := firstSegment; segment <= lastSegment; segment++ {
		if !segmentShardOwns(segment, count, index) {
			continue
		}
		lo := segment * HeaderSegmentSize
		hi := lo + HeaderSegmentSize
		if lo < start {
			lo = start
		}
		if hi > end {
			hi = end
		}
		blocks += hi - lo
	}
	return blocks
}

func feedBlockRange(
	ctx context.Context,
	input *parallelReplayInput,
	chainCfg *params.ChainConfig,
	r replayInputRange,
	out chan<- WitnessJob,
	reservoir *replayInputReservoir,
) error {
	hashStart := r.start
	if hashStart > blockHashWindowSize {
		hashStart -= blockHashWindowSize
	} else {
		hashStart = 0
	}
	hashes := make([]types.Hash, r.end-hashStart)
	headers := make([]*block.Header, r.end-r.start)
	for n := hashStart; n < r.end; n++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := input.hb.header(n)
		if err != nil {
			return fmt.Errorf("read header %d: %w", n, err)
		}
		hashes[n-hashStart] = hdr.Hash()
		if n >= r.start {
			headers[n-r.start] = hdr
		}
	}

	for blockNum := r.start; blockNum < r.end; blockNum++ {
		hdr := headers[blockNum-r.start]
		var body *GethBodyResult
		var err error
		if consuming, ok := input.hb.(parallelConsumingHeadersBodiesSource); ok {
			body, err = consuming.takeBodyNoAhead(blockNum)
		} else {
			body, err = input.hb.body(blockNum)
		}
		if err != nil {
			return fmt.Errorf("read body %d: %w", blockNum, err)
		}
		witnessData, err := input.witness.RetrieveSequential(blockNum)
		if err != nil {
			return fmt.Errorf("read witness %d: %w", blockNum, err)
		}

		var senders []types.Address
		if input.senders != nil && blockNum < input.senders.Items() {
			if data, readErr := input.senders.RetrieveSequential(blockNum); readErr == nil {
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
				if sender, senderErr := transaction.Sender(signer, txn); senderErr == nil {
					senders[i] = sender
				}
			}
		}

		job := WitnessJob{
			BlockNum:    blockNum,
			Header:      hdr,
			Body:        body,
			Witness:     witnessData,
			Senders:     senders,
			BlockHashFn: makeRangeBlockHashFn(blockNum, hashStart, hashes),
		}
		if reservoir != nil {
			job.inputBytes = estimateWitnessJobBytes(&job)
			if err := reservoir.reserve(ctx, job.inputBytes); err != nil {
				return err
			}
			job.inputReservoir = reservoir
		}
		select {
		case out <- job:
		case <-ctx.Done():
			job.releaseInputReservation()
			return ctx.Err()
		}
	}
	return nil
}

func makeRangeBlockHashFn(current, hashStart uint64, hashes []types.Hash) func(uint64) types.Hash {
	return func(n uint64) types.Hash {
		if n < hashStart || n >= current || current-n > blockHashWindowSize {
			return types.Hash{}
		}
		return hashes[n-hashStart]
	}
}

// _ block.Header silence-import if needed
var _ = block.Header{}
