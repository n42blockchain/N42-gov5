// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// executor.go — batch block executor that drives the eth-el replay.
//
// Executor reads headers, bodies, senders and receipts from a Geth-format
// input Freezer, re-executes every transaction through the shared
// ProcessBlock path, and writes changesets (acctcs/storcs) and block
// witness to an output Freezer. A PlainStateBuffer accumulates state
// mutations between commit boundaries so that MDBX only sees one
// batch per CommitInterval. VerifyInterval toggles periodic state-root
// verification; NoOutputs skips output writing entirely.

package ethel

import (
	"context"
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
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
	// NoWitness if true, skip ZK witness recording while still emitting
	// other outputs (receipts, senders, changesets). Witness wraps every
	// state read in an extra fn call + slice append; disabling it on
	// runs that don't need ZK proofs measurably reduces GC pressure.
	NoWitness bool
	// ParallelEVM if true, use Block-STM parallel EVM for tx execution.
	// EXPERIMENTAL — see docs/parallel_evm_plan.md. Default false keeps
	// the sequential path; the parallel path is opt-in and additive.
	ParallelEVM bool
	// ParallelWorkers is the Block-STM worker count when ParallelEVM is on.
	// 0 defaults to 8 (sweet spot per cmd/conflict-analyze).
	ParallelWorkers int
	// TimingInterval controls how often the P50/P99 log line is emitted.
	// 0 defaults to 10000 blocks. Set higher to reduce log noise on long
	// forward-replays; set lower for finer-grained profiling.
	TimingInterval int
	// PrefetchSpeculative enables the reth-style speculative prewarm path
	// in the prefetcher (run block N's txs through a NoopWriter to warm
	// the LRU cache with everything internal CALL/SLOAD touches). When
	// false the prefetcher uses the static AccessList path. Default true.
	PrefetchSpeculative bool
	// PrefetchEnabled is the master switch for the background prefetcher
	// (both static-AL and speculative paths). Set to false to bisect
	// prefetcher-induced LRU races. Default true.
	PrefetchEnabled bool
	// PrewarmAccessList runs main-thread synchronous AccessList prewarm
	// at the start of every block — the LRU-warming path that replaced
	// the prefetcher's removed storage LRU writes. See prewarm.go for
	// the race-safety argument and prefetchStateReader.ReadAccountStorage
	// for the race that motivated this split. Default true.
	PrewarmAccessList bool
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
	asyncFlush       *asyncFlusher // background PlainStateBuffer flusher

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
	asyncOut   *asyncOutputWriter // background changeset encoder + writer

	// Timing collection for P50/P99 analysis.
	timingSamples []timingSample
	timingWindow  int // samples per window (default 1000)

	// Optional hook called instead of state-root verify at VerifyInterval
	// boundaries. When set, the standard VerifyStateRoot path is skipped.
	verifyHook func(blockNum uint64) error

	// currentBlockNum is published before each block executes so the
	// speculative prefetcher can detect when the executor catches up and
	// abort work on the prefetched block.
	currentBlockNum atomic.Uint64

	// txOpenEpoch is the value of stateBuf.flushEpoch at the moment the
	// current main-loop RoTx was opened. Refreshed on every RoTx
	// rotation (initial BeginRo + each commit-boundary rotation) so
	// per-block writers can pin the horizon at which their MDBX snapshot
	// is consistent with wipedAtEpoch stamps. See
	// modules/state.NewBufferedPlainStateWriterAt and the 10941306
	// storcs-stale-row regression for the race this closes.
	txOpenEpoch uint64
}

// beginRoAndCaptureEpoch opens a fresh main-loop RoTx and stamps
// e.txOpenEpoch with the current flushEpoch. Encapsulating the pair
// makes "RoTx ↔ epoch capture" a structural invariant — a future
// callsite that opens a RoTx by hand and forgets the capture would
// silently re-introduce the 10941306 stale-RoTx phantom-changeset
// regression.
func (e *Executor) beginRoAndCaptureEpoch(ctx context.Context) (kv.Tx, error) {
	tx, err := e.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	e.txOpenEpoch = e.stateBuf.CurrentFlushEpoch()
	return tx, nil
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
	// No commit-interval alignment needed: retrieveBatch now finds
	// batch boundaries by scanning cidx offsets instead of assuming
	// 64-aligned positions. Partial batches from flushAll are safe.
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

// SetCacheBudget rebuilds the PlainStateBuffer with the given cache
// budget. Must be called before Run (the buffer is mutable state and
// rebuilding mid-run would lose pending writes).
func (e *Executor) SetCacheBudget(b state.CacheBudget) {
	e.stateBuf = state.NewPlainStateBufferWithBudget(b)
}

// SetVerifyHook installs a custom verify callback that replaces the
// standard state-root verification at VerifyInterval boundaries.
func (e *Executor) SetVerifyHook(fn func(blockNum uint64) error) {
	e.verifyHook = fn
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

	// One-time setup uses a write tx; the main exec loop later
	// switches to a RoTx so the background flusher can hold its own
	// RwTx concurrently. The setup tx covers ReadProgress + the
	// optional InitHashState pre-fill below; both fit in a single
	// short transaction.
	setupTx, err := e.db.BeginRw(ctx)
	if err != nil {
		return err
	}
	startBlock := e.cfg.StartBlock
	if saved := ReadProgress(setupTx); saved > 0 && saved >= startBlock {
		startBlock = saved + 1
		log.Info("Resuming execution", "from", startBlock, "lastCommitted", saved)
	}
	// Caller may set the cleanup chain for the loop tx below.
	var tx kv.Tx
	cleanup := func() {}
	defer func() { cleanup() }()

	if startBlock > endBlock {
		setupTx.Rollback()
		log.Info("Already past target", "at", startBlock-1, "target", endBlock)
		return nil
	}

	// Initialize HashedAccounts from PlainState if verify enabled and not yet populated.
	if e.cfg.VerifyInterval > 0 {
		if err := InitHashState(setupTx); err != nil {
			setupTx.Rollback()
			return fmt.Errorf("init hash state: %w", err)
		}
	}

	// Initialize output batcher and align tables to startBlock.
	if e.outFreezer != nil && !e.cfg.NoOutputs {
		batcher, err := newOutputBatcher(e.outFreezer)
		if err != nil {
			setupTx.Rollback()
			return fmt.Errorf("init output batcher: %w", err)
		}
		e.outBatcher = batcher
		defer batcher.Close()
		// Read MDBX-saved remainder BEFORE alignOnResume. If present,
		// alignOnResume skips freezer partial-batch recovery (the MDBX
		// remainder is the authoritative source, saved atomically with
		// state). Without this, both paths restore overlapping entries
		// causing position misalignment.
		csRem := ReadCSRemainder(setupTx)
		if err := batcher.alignOnResume(startBlock, len(csRem) > 0); err != nil {
			setupTx.Rollback()
			return fmt.Errorf("align output tables: %w", err)
		}
		if len(csRem) > 0 {
			batcher.preloadRemainder(csRem)
		}
	}

	// Commit the setup tx and switch to a RoTx for the main loop.
	if err := setupTx.Commit(); err != nil {
		return fmt.Errorf("commit setup tx: %w", err)
	}
	tx, err = e.beginRoAndCaptureEpoch(ctx)
	if err != nil {
		return err
	}
	cleanup = func() { tx.Rollback() }

	startTime := time.Now()
	e.lastProgressTime = startTime

	// Start background prefetcher to warm MDBX page cache. Pass the
	// pre-computed senders sources so prefetcher doesn't waste time
	// re-doing ecrecover that the executor already avoids. Master switch
	// PrefetchEnabled lets us bisect prefetcher-induced LRU races by
	// running the executor with no background prewarm at all.
	if e.cfg.PrefetchEnabled {
		e.prefetcher = newPrefetcher(ctx, e.freezer, e.db, e.stateBuf, e.chainCfg, e.senderStore, e.senderTable, e.engine, &e.currentBlockNum, e.cfg.PrefetchSpeculative)
		e.prefetcher.start()
		defer e.prefetcher.stop()
	} else {
		log.Info("Prefetcher disabled (--prefetch=false)")
	}

	// Start background output writer (changeset encoding + freezer I/O).
	if e.outBatcher != nil {
		e.asyncOut = newAsyncOutputWriter(e.outBatcher)
		defer func() {
			if e.asyncOut != nil {
				e.asyncOut.stop()
			}
		}()
	}

	// Start the background PlainStateBuffer flusher. At every commit
	// interval the executor hands off the buffer snapshot and the bg
	// goroutine opens its OWN RwTx to apply + commit. Main thread
	// keeps a separate RoTx for reads, hiding the ~25 s flush time at
	// the 11M-block scale.
	e.asyncFlush = newAsyncFlusher(e.stateBuf, e.db, ctx)
	var lastFlushDur time.Duration
	// Per-interval cache stat baselines so cacheHit% reflects the LAST
	// commit interval, not the cumulative average since startup.
	var prevHits, prevMisses uint64
	var prevAH, prevAM, prevSH, prevSM, prevCH, prevCM uint64

	lastOKBlock := startBlock - 1 // tracks last successfully executed block
	shuttingDown := false
	for blockNum := startBlock; blockNum <= endBlock; blockNum++ {
		if ctx.Err() != nil {
			// Don't exit immediately — continue to the next commit
			// boundary so output + MDBX are durably committed together.
			// This avoids page-cache-only cdat data that Windows may
			// discard on process exit.
			if !shuttingDown {
				shuttingDown = true
				log.Info("Shutdown requested, finishing current interval...",
					"lastBlock", blockNum-1, "nextCommit", (blockNum/e.cfg.CommitInterval+1)*e.cfg.CommitInterval)
			}
		}

		// Publish the block number we're about to execute so the
		// speculative prefetcher can detect when we've caught up to a
		// block it's still prewarming and abort.
		e.currentBlockNum.Store(blockNum)

		// Prefetch lookahead 2 blocks. Speculative prewarm runs each
		// block's full EVM through a NoopWriter — at blk/s ≥ 80 the
		// per-block budget is ~12 ms, uncomfortably close to the
		// speculative EVM's own 8-10 ms cost on DeFi-era blocks.
		// Single-block lookahead loses the race frequently (sto%
		// capped ~93%); 2-block lookahead gives the prefetcher a full
		// block of slack.
		//
		// To avoid duplicate enqueues (each iter would otherwise
		// re-enqueue N+1, which the previous iter already enqueued as
		// N+2), prime the pump on the first iteration with N+1, and on
		// every iteration enqueue only N+2 — N+1 is always carried
		// over from the previous iter's N+2. Channel capacity 3 in
		// newPrefetcher absorbs transient pile-up.
		if e.prefetcher != nil {
			if blockNum == startBlock && blockNum+1 <= endBlock {
				e.prefetcher.prefetchBlock(blockNum + 1)
			}
			if blockNum+2 <= endBlock {
				e.prefetcher.prefetchBlock(blockNum + 2)
			}
		}

		var execErr error
		if e.cfg.ParallelEVM {
			execErr = e.executeBlockParallel(ctx, tx, blockNum)
		} else {
			execErr = e.executeBlock(ctx, tx, blockNum)
		}
		if execErr != nil {
			if e.cfg.SkipErrors {
				log.Warn("Block execution error (skipped)", "block", blockNum, "err", execErr)
				continue
			}
			return fmt.Errorf("block %d: %w", blockNum, execErr)
		}
		lastOKBlock = blockNum

		// Periodic flush + commit.
		if blockNum > 0 && blockNum%e.cfg.CommitInterval == 0 {
			// 1. Wait for the previous async flush to complete BEFORE
			// handing off the new one. Steady-state: this is zero-cost
			// because exec_time >= flush_time at the 11M-block scale.
			dur, err := e.asyncFlush.waitPrev()
			if err != nil {
				return fmt.Errorf("wait prev flush at block %d: %w", blockNum, err)
			}
			if dur > 0 {
				lastFlushDur = dur
			}

			// 2. Drain async output writer so all pending encodes land
			// in the batcher before we touch it from the main goroutine.
			if e.asyncOut != nil {
				if err := e.asyncOut.waitDrain(); err != nil {
					return fmt.Errorf("drain async output at block %d: %w", blockNum, err)
				}
			}
			// Save ALL pending entries to MDBX (the authoritative store
			// that survives any shutdown). Then fsync freezer.
			//
			// freezer fsync was previously "best-effort" (errors swallowed)
			// on the theory that alignOnResume would heal any mismatch on
			// restart. That masked real problems: a failing fsync left
			// users unable to tell whether cdat was actually durable. We
			// now surface the error — alignOnResume still runs on restart
			// so correctness is preserved, but the operator sees the root
			// cause immediately.
			var csEntries map[string][]byte
			if e.outBatcher != nil {
				csEntries = e.outBatcher.remainder() // ALL pending entries
				if _, err := e.outBatcher.flushFullBatches(); err != nil {
					return fmt.Errorf("flush output batcher at block %d: %w", blockNum, err)
				}
				if err := e.outBatcher.sync(); err != nil {
					return fmt.Errorf("fsync output freezer at block %d: %w", blockNum, err)
				}
			}

			// 3. Capture stats BEFORE the snapshot moves the maps.
			bufAccs, bufStos := e.stateBuf.Stats()
			cacheHits, cacheMisses := e.stateBuf.CacheStats()
			ah, am, sh, sm, ch, cm := e.stateBuf.TierStats()
			// Per-interval deltas — the bare counters are cumulative.
			intHits := cacheHits - prevHits
			intMisses := cacheMisses - prevMisses
			intAH, intAM := ah-prevAH, am-prevAM
			intSH, intSM := sh-prevSH, sm-prevSM
			intCH, intCM := ch-prevCH, cm-prevCM
			prevHits, prevMisses = cacheHits, cacheMisses
			prevAH, prevAM = ah, am
			prevSH, prevSM = sh, sm
			prevCH, prevCM = ch, cm

			// Flush diagnostic logs so a crash inside the bg flusher
			// doesn't lose recent history. No-op when not initialized.
			state.FlushDeletesLog()
			state.FlushAuditLog()

			// 4. Hand the buffer + changeset remainder to the bg
			// flusher. The remainder is saved to MDBX atomically
			// with the state + progress in the same RwTx.
			// SnapshotForFlush atomically resets the active buffer
			// AND installs the snapshot as in-flight so the reader
			// path can still find dirty values during the bg commit
			// window.
			if err := e.asyncFlush.handWithRemainder(blockNum, csEntries); err != nil {
				return fmt.Errorf("hand to async flusher at block %d: %w", blockNum, err)
			}

			// 5. Rotate main thread's RoTx. Skip during shutdown —
			// ctx is cancelled so BeginRo(ctx) would fail, and we
			// don't need fresh reads since we're about to exit.
			if !shuttingDown {
				tx.Rollback()
				tx, err = e.beginRoAndCaptureEpoch(ctx)
				if err != nil {
					cleanup = func() {}
					return err
				}
				cleanup = func() { tx.Rollback() }
			}

			// 6. Progress log (uses the PREVIOUS flush's duration; the
			// current handoff is still in flight).
			now := time.Now()
			intervalSec := now.Sub(e.lastProgressTime).Seconds()
			if intervalSec < 0.001 {
				intervalSec = 0.001
			}
			blkPerSec := float64(e.cfg.CommitInterval) / intervalSec
			e.lastProgressTime = now
			hitPct := func(h, m uint64) string {
				if h+m == 0 {
					return "-"
				}
				return fmt.Sprintf("%.1f", float64(h)/float64(h+m)*100)
			}
			fields := []interface{}{
				"block", blockNum,
				"blk/s", fmt.Sprintf("%.0f", blkPerSec),
				"elapsed", now.Sub(startTime).Truncate(time.Second),
				"bufFlush", lastFlushDur.Truncate(time.Millisecond),
				"bufAccs", bufAccs, "bufStos", bufStos,
				"hit%", hitPct(intHits, intMisses),
				"acct%", hitPct(intAH, intAM),
				"sto%", hitPct(intSH, intSM),
				"code%", hitPct(intCH, intCM),
				"async", "y",
			}
			if e.senderMisses > 0 {
				fields = append(fields, "senderMiss", e.senderMisses)
				e.senderMisses = 0
			}
			log.Info("EthEL progress", fields...)

			// 7. Verify state root at verify boundaries. Verify needs
			// the data to be visible in MDBX (and writes intermediate
			// hashed-state tables), so we MUST wait for the in-flight
			// bg flush to complete and then run verify under its own
			// short-lived RwTx.
			//
			// WARNING: the standard-MPT verify path (internal/ethcompat/
			// state_root.go) builds the full account trie + per-account
			// storage tries in Go heap. At block 15M state has ~10M+
			// active accounts and the transient allocations during
			// construction can exceed 30 GB. Combined with MDBX dirty
			// pages, page cache, and a GOGC=400 heap, a 128 GB host
			// can OOM. For archive replay past block 10M: DO NOT use
			// --verify with a periodic interval. Use the one-shot
			// verify-root subcommand after run completion instead.
			const verifyBlockCap = 10_000_000
			if e.cfg.VerifyInterval > 0 && blockNum > verifyBlockCap && e.verifyHook == nil {
				log.Warn("Skipping periodic --verify past block 10M to avoid OOM; use verify-root subcommand instead",
					"block", blockNum, "cap", verifyBlockCap)
			}
			if e.cfg.VerifyInterval > 0 && blockNum%e.cfg.VerifyInterval == 0 &&
				!shuttingDown &&
				(blockNum <= verifyBlockCap || e.verifyHook != nil) {
				dur, err := e.asyncFlush.waitPrev()
				if err != nil {
					return fmt.Errorf("wait pre-verify flush at block %d: %w", blockNum, err)
				}
				if dur > 0 {
					lastFlushDur = dur
				}

				if e.verifyHook != nil {
					// Custom verify hook (e.g., compare-mode CS→shadow diff).
					if err := e.verifyHook(blockNum); err != nil {
						return err
					}
				} else {
				// Release main RoTx so we can open a RwTx for verify.
				tx.Rollback()
				verifyTx, vErr := e.db.BeginRw(ctx)
				if vErr != nil {
					cleanup = func() {}
					return vErr
				}
				blockRoot, verifyErr := VerifyStateRoot(verifyTx)
				if verifyErr != nil {
					verifyTx.Rollback()
					return fmt.Errorf("state root verify at block %d: %w", blockNum, verifyErr)
				}
				if cErr := verifyTx.Commit(); cErr != nil {
					return fmt.Errorf("commit verify tx at block %d: %w", blockNum, cErr)
				}
				// Reopen main RoTx so the next exec interval sees the
				// newly committed hashed-state tables.
				tx, err = e.beginRoAndCaptureEpoch(ctx)
				if err != nil {
					cleanup = func() {}
					return err
				}
				cleanup = func() { tx.Rollback() }
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
				} // end else (standard verify)
			}
			// Exit after commit if shutdown was requested.
			if shuttingDown {
				log.Info("Shutdown complete after commit", "block", blockNum)
				break
			}
		}
	}

	// Final flush + commit.
	//
	// Drain the in-flight bg flush (from the last commit boundary).
	if _, err := e.asyncFlush.waitPrev(); err != nil {
		return fmt.Errorf("wait final pending flush: %w", err)
	}

	if !shuttingDown {
		// Drain async output writer before touching the batcher.
		if e.asyncOut != nil {
			if err := e.asyncOut.stop(); err != nil {
				return fmt.Errorf("stop async output: %w", err)
			}
			e.asyncOut = nil
		}
		// Normal termination (--end reached): save all entries to MDBX,
		// then fsync freezer.
		var finalEntries map[string][]byte
		if e.outBatcher != nil {
			finalEntries = e.outBatcher.remainder()
			if _, err := e.outBatcher.flushFullBatches(); err != nil {
				return fmt.Errorf("flush output batcher final: %w", err)
			}
			if err := e.outBatcher.sync(); err != nil {
				return fmt.Errorf("fsync output freezer final: %w", err)
			}
		}
		if err := e.asyncFlush.handWithRemainder(lastOKBlock, finalEntries); err != nil {
			return fmt.Errorf("hand final flush: %w", err)
		}
		if _, err := e.asyncFlush.waitPrev(); err != nil {
			return fmt.Errorf("wait final flush: %w", err)
		}
	}
	// Shutdown path: the commit at the break boundary already saved
	// state + progress + remainder. Nothing more to do — waitPrev
	// above ensured it completed.
	e.stateBuf.ClearInFlight()

	// Main RoTx is now stale; rotate so any caller using the executor
	// after Run() returns observes the latest committed state.
	tx.Rollback()
	cleanup = func() {} // bg already committed; nothing left to roll back

	return nil
}

// executeBlock processes a single block.
//
// tx is kv.Tx (read-only) — block execution only reads state from MDBX
// (writes go to PlainStateBuffer). The async commit-interval flusher
// is the only path that opens a kv.RwTx.
func (e *Executor) executeBlock(ctx context.Context, tx kv.Tx, blockNum uint64) error {
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
	if e.outFreezer != nil && !e.cfg.NoOutputs && !e.cfg.NoWitness {
		witnessReader = NewWitnessStateReader(bufReader)
		reader = witnessReader
	}
	// Writer goes to in-memory buffer (no MDBX Put per block). Pass
	// e.txOpenEpoch (captured at the most recent BeginRo) so
	// collectPreWipeSlots can detect post-flush wipes that the
	// long-lived RoTx hasn't picked up — block 10941306 storcs
	// regression.
	writer := state.NewBufferedPlainStateWriterAt(e.stateBuf, tx, blockNum, e.txOpenEpoch)
	ibs := state.New(reader)

	// State root verification moved to main loop (Run) for rollback support.

	// 2.5 Sync staticAL prewarm — race-free single-goroutine replacement
	// for the prefetcher's (now removed) storage LRU writes. Reads each
	// AccessList slot through bufReader, which Put's the value into the
	// shared read LRU on MDBX miss. Subsequent EVM SLOAD hits warm cache.
	//
	// Intentional overlap with the bg prefetcher's MDBX page-warm: bg
	// covers next-block N+1 (lookahead), main covers current N (LRU
	// fill). Repeated reads on the same warm page are <1µs.
	if e.cfg.PrewarmAccessList {
		prewarmStaticAL(bufReader, body.Transactions)
	}

	t1 := time.Now()
	// 3. Process block (DAO fork, system contracts, EVM).
	var senders []types.Address
	senders = e.loadSenders(blockNum, len(body.Transactions))

	// (Legacy RunTxDiff harness removed — required a RwTx; the per-block
	// path is RoTx-only now.)

	result, err := e.processBlock(header, body, ibs, senders, writer)
	if err != nil {
		// On gas limit exhaustion, processBlock returns a partial result
		// with receipts up to the failing tx — use it for diagnostics.
		if result != nil {
			e.dumpGasMismatch(blockNum, header, body, result)
		}
		return err
	}

	// reth-style per-block LRU refresh. Pushes every (addr, slot, code)
	// this block touched into the shared read caches, so the next
	// block's reads can hit cache instead of falling through to MDBX.
	// Without this, recently written entries miss the LRU for the rest
	// of the commit interval (~10K blocks) and only get bulk-promoted
	// at the next interval boundary, dragging hit rate down with the
	// growing buffer (sto% observed declining 78→66 across 2.2M blocks
	// of a single interval before this change).
	writer.RefreshLRUForBlock()

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
	rules := e.chainCfg.Rules(header.Number.Uint64())
	if err := ibs.CommitBlock(rules, writer); err != nil {
		return fmt.Errorf("commit block state: %w", err)
	}
	t3 := time.Now()

	// 7. Snapshot output data and enqueue for async encoding + write.
	// The snapshot reads from stateBuf SYNCHRONOUSLY (before the next
	// block can modify it), so the background encoder never races.
	if e.asyncOut != nil {
		if err := e.asyncOut.checkError(); err != nil {
			return err
		}
		po, err := e.snapshotOutputs(blockNum, result, writer, witnessReader, tx)
		if err != nil {
			return fmt.Errorf("snapshot outputs: %w", err)
		}
		e.asyncOut.enqueue(po)
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
	return ProcessBlock(e.chainCfg, e.engine, header, body.Transactions, uncles, body.Withdrawals, ibs, e.makeBlockHashFunc(header), senders, writer)
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
		if e.cfg.TimingInterval > 0 {
			e.timingWindow = e.cfg.TimingInterval
		} else {
			e.timingWindow = 10000
		}
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
	var totalDur time.Duration
	for _, v := range s {
		totalGas += v.gasUsed
		totalTx += v.txCount
		totalDur += v.total
	}
	// Mgas/s throughput across the window. Guard against div-by-zero
	// when the window's total elapsed time rounds to 0.
	mgasPerSec := 0.0
	if totalDur > 0 {
		mgasPerSec = float64(totalGas) / totalDur.Seconds() / 1e6
	}

	ms := func(d time.Duration) string { return fmt.Sprintf("%.1f", float64(d.Microseconds())/1000) }
	log.Info("P50/P99",
		"block", blockNum,
		"tx", totalTx/n,
		"gas", fmt.Sprintf("%.1f", mgasPerSec),
		"P50", ms(p50.total),
		"P50evm", ms(p50.evm),
		"P99", ms(p99.total),
		"P99evm", ms(p99.evm),
		"P99commit", ms(p99.commit),
		"P99out", ms(p99.outputs),
		"worst", ms(worst.total),
	)
}

// snapshotOutputs captures everything the background encoder needs from
// the post-commit state. This runs SYNCHRONOUSLY on the main goroutine
// so the next block's execution cannot race with stateBuf reads.
// Cost: map iteration + map lookups in stateBuf — typically < 0.5ms.
func (e *Executor) snapshotOutputs(blockNum uint64, result *BlockResult, writer *state.BufferedPlainStateWriter, witness *WitnessStateReader, dbTx kv.Tx) (pendingOutput, error) {
	po := pendingOutput{blockNum: blockNum}

	// Block witness (already encoded, block-scoped).
	if witness != nil {
		po.witnessData = witness.Encode()
	}

	// Block 0: genesis encoding needs dbTx iterators — encode synchronously
	// here (one-time cost) and pass pre-encoded bytes.
	if blockNum == 0 {
		var err error
		po.genesisAcct, err = EncodeGenesisAccounts(newGenesisAccountIterator(dbTx))
		if err != nil {
			return po, fmt.Errorf("encode genesis accounts: %w", err)
		}
		po.genesisSto, err = EncodeGenesisStorages(newGenesisStorageIterator(dbTx))
		if err != nil {
			return po, fmt.Errorf("encode genesis storages: %w", err)
		}
		return po, nil
	}

	csw := writer.ChangeSetWriter()
	if csw == nil {
		return po, nil
	}

	accCS, err := csw.GetAccountChanges()
	if err != nil {
		return po, fmt.Errorf("get account changes: %w", err)
	}
	stoCS, err := csw.GetStorageChanges()
	if err != nil {
		return po, fmt.Errorf("get storage changes: %w", err)
	}
	po.accCS = accCS
	po.stoCS = stoCS

	// Snapshot new values by reading from BufferedPlainStateReader.
	// This is the ONLY place that touches stateBuf — done synchronously
	// so the next block's writes cannot interfere.
	bufReader := state.NewBufferedPlainStateReader(e.stateBuf, dbTx)

	po.accNewVals = make(map[types.Address][]byte, accCS.Len())
	for _, c := range accCS.Changes {
		if len(c.Key) < 20 {
			continue
		}
		var addr types.Address
		copy(addr[:], c.Key[:20])
		a, err := bufReader.ReadAccountData(addr)
		if err != nil || a == nil {
			po.accNewVals[addr] = nil
		} else {
			// Reth-style: full V2 (with CodeHash, no hidden incarnation tag).
			po.accNewVals[addr] = a.MarshalV2()
		}
	}

	po.stoNewVals = make(map[[52]byte][]byte, stoCS.Len())
	for _, c := range stoCS.Changes {
		if len(c.Key) < 52 {
			continue
		}
		var addr types.Address
		var slot types.Hash
		copy(addr[:], c.Key[:20])
		copy(slot[:], c.Key[20:52])
		v, err := bufReader.ReadAccountStorage(addr, &slot)
		if err != nil {
			v = nil
		}
		if len(v) > 32 {
			panic(fmt.Sprintf("snapshotOutputs: bufReader.ReadAccountStorage returned len=%d > 32 (block=%d addr=%x slot=%x)",
				len(v), blockNum, addr, slot))
		}
		if len(c.Value) > 32 {
			panic(fmt.Sprintf("snapshotOutputs: stoCS.Changes c.Value len=%d > 32 (block=%d addr=%x slot=%x) — csw was polluted",
				len(c.Value), blockNum, addr, slot))
		}
		var key [52]byte
		copy(key[:], c.Key[:52])
		if len(v) > 0 {
			cp := make([]byte, len(v))
			copy(cp, v)
			po.stoNewVals[key] = cp
		} else {
			po.stoNewVals[key] = nil
		}
	}

	return po, nil
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
			var (
				txHash  types.Hash
				toStr   = "create"
				dataLen int
				value   = "0"
				gasLim  uint64
				nonce   uint64
			)
			if i < len(body.Transactions) {
				txn := body.Transactions[i]
				txHash = txn.Hash()
				if to := txn.To(); to != nil {
					toStr = to.Hex()
				}
				dataLen = len(txn.Data())
				if v := txn.Value(); v != nil {
					value = v.String()
				}
				gasLim = txn.Gas()
				nonce = txn.Nonce()
			}
			n42LogsHash := receiptLogsHash(result.Receipts[i])
			gethLogsHash := receiptLogsHash(gethReceipts[i])
			log.Error("Gas-mismatch tx",
				"block", blockNum,
				"txIndex", i,
				"txHash", txHash.Hex(),
				"to", toStr,
				"value", value,
				"nonce", nonce,
				"gasLimit", gasLim,
				"dataLen", dataLen,
				"n42Gas", n42Tx,
				"gethGas", gethTx,
				"diff", d,
				"txType", result.Receipts[i].Type,
				"status", result.Receipts[i].Status,
				"n42Logs", len(result.Receipts[i].Logs),
				"gethLogs", len(gethReceipts[i].Logs),
				"logsMatch", n42LogsHash == gethLogsHash,
			)
		}
	}
	log.Error("Gas-mismatch totals",
		"block", blockNum,
		"diffs", totalDiffs,
		"sumDiff", totalDiffSum,
		"n42Receipts", len(result.Receipts),
		"gethReceipts", len(gethReceipts),
		"n42Gas", result.GasUsed,
		"gethGas", header.GasUsed,
	)
}

// receiptLogsHash returns a stable hash over a receipt's logs (address +
// topics + data) so a gas-mismatch dump can flag whether the divergent
// tx also produced different logs — a strong tell for state divergence
// vs a pure gas-accounting issue.
func receiptLogsHash(r *block.Receipt) types.Hash {
	if r == nil || len(r.Logs) == 0 {
		return types.Hash{}
	}
	h := crypto.NewKeccakState()
	for _, lg := range r.Logs {
		h.Write(lg.Address[:])
		for _, t := range lg.Topics {
			h.Write(t[:])
		}
		h.Write(lg.Data)
	}
	var out types.Hash
	h.Read(out[:])
	return out
}
