// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// compare-root helper: cross-check state roots at a given block.
// Computes the root via the traditional MPT FlatDBTrieLoader over
// HashedAccounts / HashedStorage and via the JMT / BMT commitment
// backends from lib/commitment, then prints a side-by-side diff.
// Used by the ethexec debug flow when a replayed block disagrees
// with the expected header state root.

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	libcommit "github.com/n42blockchain/N42/lib/commitment"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// runCompareRoot computes the state root at a given block using two methods:
//
//  1. Traditional MPT (FlatDBTrieLoader over HashedAccounts/HashedStorage)
//  2. HPH (HexPatriciaHashed via MPTRootComputer) bulk-rebuilt from PlainState
//
// and compares both against the header root at that block.
//
// Assumes PlainState in --datadir is already populated up to --block
// (e.g. via `ethexec rebuild-state ...`).
func runCompareRoot(c *cli.Context) error {
	datadir := c.String("datadir")
	ancientPath := c.String("ancient")
	block := c.Uint64("block")
	mode := c.String("mode")
	concurrent := c.Bool("concurrent")
	tmpdir := c.String("tmpdir")
	dirtyGB := c.Uint64("dirty-space-gb")
	if dirtyGB == 0 {
		dirtyGB = 32
	}

	noHeader := c.Bool("no-header")

	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(datadir).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(4 * datasize.TB).
		GrowthStep(4 * datasize.GB).
		DirtySpace(uint64(datasize.ByteSize(dirtyGB) * datasize.GB)).
		Accede().
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open mdbx: %w", err)
	}
	defer db.Close()

	ctx, cancel := withShutdown()
	defer cancel()

	// Header root from geth ancient (skipped in --no-header mode so this
	// can run as a one-shot HPH bulk-rebuild without needing a geth
	// ancient on disk — used for lazy-build of CommitmentBranches against
	// PlainState that was populated by another tool).
	var headerRoot types.Hash
	if !noHeader {
		f, err := freezer.New(ancientPath, 0)
		if err != nil {
			return fmt.Errorf("open ancient: %w", err)
		}
		defer f.Close()
		hdrData, err := f.Ancient(freezer.TableHeaders, block)
		if err != nil {
			return fmt.Errorf("read header %d: %w", block, err)
		}
		hdr, err := ethel.DecodeGethHeader(hdrData)
		if err != nil {
			return fmt.Errorf("decode header %d: %w", block, err)
		}
		headerRoot = hdr.Root
		fmt.Printf("\n=== Comparing state root @ block %d ===\n", block)
		fmt.Printf("Header root: %s\n\n", headerRoot.Hex())
	} else {
		fmt.Printf("\n=== Bulk-rebuild state root @ block %d (--no-header: skip header compare) ===\n", block)
	}

	var (
		rootMPT, rootHPH types.Hash
		dHash, dMPT, dHPH time.Duration
		mpAllocMB, hpAllocMB uint64
	)

	if mode == "mpt" || mode == "both" {
		log.Info("=== Method A: Traditional MPT (FlatDBTrieLoader) ===")
		runtime.GC()
		var m0 runtime.MemStats
		runtime.ReadMemStats(&m0)

		// Phase 1: (re)populate HashedAccounts/HashedStorage from PlainState.
		// CalcStateRoot requires these tables to be up-to-date. The ETL
		// path uses sorted bulk-load instead of 240M+ random Puts —
		// orders of magnitude faster on 10M+ block states.
		tHash := time.Now()
		{
			tx, err := db.BeginRw(ctx)
			if err != nil {
				return err
			}
			if err := ethel.RebuildHashedStateETL(ctx, tx, tmpdir, logger); err != nil {
				tx.Rollback()
				return fmt.Errorf("rebuild hashed state: %w", err)
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit hashed state: %w", err)
			}
		}
		dHash = time.Since(tHash)
		log.Info("HashedState rebuilt", "elapsed", dHash.Truncate(time.Millisecond))

		// Phase 2: CalcStateRoot clears + rebuilds TrieOfAccounts/TrieOfStorage.
		tx, err := db.BeginRw(ctx)
		if err != nil {
			return err
		}
		tMPT := time.Now()
		r, err := ethel.CalcStateRoot(tx)
		dMPT = time.Since(tMPT)
		tx.Rollback()
		if err != nil {
			return fmt.Errorf("CalcStateRoot: %w", err)
		}
		rootMPT = r
		var m1 runtime.MemStats
		runtime.ReadMemStats(&m1)
		mpAllocMB = (m1.Alloc - m0.Alloc) / 1e6
		log.Info("Method A done",
			"root", rootMPT.Hex(),
			"hashedStateBuild", dHash.Truncate(time.Millisecond),
			"calcTrieRoot", dMPT.Truncate(time.Millisecond),
			"total", (dHash + dMPT).Truncate(time.Millisecond),
			"allocDeltaMB", mpAllocMB)
	}

	if mode == "hph" || mode == "both" {
		if concurrent {
			log.Info("=== Method B: HPH (ConcurrentMPTRootComputer 16-way parallel bulk rebuild) ===")
		} else {
			log.Info("=== Method B: HPH (MPTRootComputer bulk rebuild) ===")
		}
		runtime.GC()
		var m0 runtime.MemStats
		runtime.ReadMemStats(&m0)

		t0 := time.Now()
		var r types.Hash
		if concurrent {
			r, err = computeHPHFromPlainStateConcurrent(ctx, db, logger)
		} else {
			r, err = computeHPHFromPlainState(ctx, db)
		}
		dHPH = time.Since(t0)
		if err != nil {
			return fmt.Errorf("HPH compute: %w", err)
		}
		rootHPH = r
		var m1 runtime.MemStats
		runtime.ReadMemStats(&m1)
		hpAllocMB = (m1.Alloc - m0.Alloc) / 1e6
		log.Info("Method B done",
			"root", rootHPH.Hex(),
			"elapsed", dHPH.Truncate(time.Millisecond),
			"allocDeltaMB", hpAllocMB)
	}

	// Comparison table.
	fmt.Println()
	fmt.Println("=== Results ===")
	if !noHeader {
		fmt.Printf("Header        : %s\n", headerRoot.Hex())
	}
	if mode == "mpt" || mode == "both" {
		ok := !noHeader && rootMPT == headerRoot
		if noHeader {
			fmt.Printf("Method A MPT  : %s\n", rootMPT.Hex())
		} else {
			fmt.Printf("Method A MPT  : %s  match=%v\n", rootMPT.Hex(), ok)
		}
		fmt.Printf("  hashedState : %s\n", dHash.Truncate(time.Millisecond))
		fmt.Printf("  calcTrieRoot: %s\n", dMPT.Truncate(time.Millisecond))
		fmt.Printf("  total       : %s   allocDelta=%dMB\n", (dHash + dMPT).Truncate(time.Millisecond), mpAllocMB)
	}
	if mode == "hph" || mode == "both" {
		ok := !noHeader && rootHPH == headerRoot
		if noHeader {
			fmt.Printf("Method B HPH  : %s\n", rootHPH.Hex())
		} else {
			fmt.Printf("Method B HPH  : %s  match=%v\n", rootHPH.Hex(), ok)
		}
		fmt.Printf("  elapsed     : %s   allocDelta=%dMB\n", dHPH.Truncate(time.Millisecond), hpAllocMB)
	}
	if mode == "both" {
		fmt.Println()
		aTotal := dHash + dMPT
		if dHPH < aTotal {
			fmt.Printf("HPH is %.2fx faster than MPT (incl. hash)\n", float64(aTotal)/float64(dHPH))
		} else {
			fmt.Printf("MPT is %.2fx faster than HPH\n", float64(dHPH)/float64(aTotal))
		}
		if dHPH < dMPT {
			fmt.Printf("HPH is %.2fx faster than MPT CalcTrieRoot alone\n", float64(dMPT)/float64(dHPH))
		} else {
			fmt.Printf("MPT CalcTrieRoot alone is %.2fx faster than HPH\n", float64(dHPH)/float64(dMPT))
		}
	}
	return nil
}

// computeHPHFromPlainState bulk-loads PlainState into HPH in address-sorted
// chunks. Each chunk interleaves one address's account + all its storage slots,
// so HPH folds each account's storage subtrie exactly once per chunk.
//
// Memory profile: ModeDirect's internal dedup map is unbounded in size, so the
// naive "touch everything, call Process once" approach would blow ~13GB on a
// full 8M-block state (260M keys). Instead we call Process() every
// hphChunkKeys touches — HashSort clears the dedup map and reinits the ETL
// collector on each call, while HPH's internal trie state persists across
// calls (its per-block incremental design). Peak memory is bounded to one
// chunk's worth of dedup + ETL buffers (~50MB).
//
// hphChunkKeys is the soft upper bound on touched keys between Process calls.
// Chunk boundaries align on address, so one large-storage address may exceed
// this bound; that's fine.
const hphChunkKeys = 500_000

func computeHPHFromPlainState(ctx context.Context, db kv.RwDB) (types.Hash, error) {
	tx, err := db.BeginRw(ctx)
	if err != nil {
		return types.Hash{}, err
	}
	defer tx.Rollback()

	reader := commitment.NewPlainStateMPTReader(tx)
	computer := commitment.NewMPTRootComputer()
	computer.SetStateReader(reader)
	trie := computer.Trie()

	tmpdir, err := os.MkdirTemp("", "hph-bulk-*")
	if err != nil {
		return types.Hash{}, err
	}
	defer os.RemoveAll(tmpdir)

	updates := libcommit.NewUpdates(libcommit.ModeDirect, tmpdir, libcommit.KeyToHexNibbleHash)

	accCur, err := tx.Cursor(modules.Account)
	if err != nil {
		return types.Hash{}, err
	}
	defer accCur.Close()

	// Separate cursor for storage prefix seek per address. Using a second
	// transaction cursor (not the same tx.Cursor instance) keeps the account
	// iterator position stable across storage probes.
	stoCur, err := tx.Cursor(modules.Storage)
	if err != nil {
		return types.Hash{}, err
	}
	defer stoCur.Close()

	var (
		accCount     int
		stoCount     int
		chunkKeys    int
		chunkIdx     int
		lastRoot     []byte
		lastLog      = time.Now()
	)

	flush := func() error {
		if chunkKeys == 0 {
			return nil
		}
		chunkIdx++
		r, err := trie.Process(ctx, updates, "compare-root", nil, libcommit.WarmupConfig{})
		if err != nil {
			return fmt.Errorf("hph process chunk %d: %w", chunkIdx, err)
		}
		lastRoot = r
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		log.Info("HPH chunk processed",
			"chunk", chunkIdx,
			"keys", chunkKeys,
			"accounts", accCount,
			"storage", stoCount,
			"allocMB", ms.Alloc/1e6)
		chunkKeys = 0
		return nil
	}

	for k, v, err := accCur.First(); k != nil; k, v, err = accCur.Next() {
		if err != nil {
			return types.Hash{}, err
		}
		if len(k) != 20 {
			continue
		}
		// Touch account.
		updates.TouchPlainKey(string(k), v, updates.TouchAccount)
		accCount++
		chunkKeys++

		// Touch all storage slots for this address via a prefix seek.
		prefix := k
		for sk, sv, err := stoCur.Seek(prefix); sk != nil; sk, sv, err = stoCur.Next() {
			if err != nil {
				return types.Hash{}, err
			}
			if len(sk) < 20 || !bytesEqualPrefix(sk, prefix) {
				break
			}
			if len(sk) != 52 {
				continue
			}
			updates.TouchPlainKey(string(sk), sv, updates.TouchStorage)
			stoCount++
			chunkKeys++
		}

		if time.Since(lastLog) > 15*time.Second {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			log.Info("HPH ingest progress",
				"accounts", accCount,
				"storage", stoCount,
				"chunkKeys", chunkKeys,
				"allocMB", ms.Alloc/1e6)
			lastLog = time.Now()
		}

		if chunkKeys >= hphChunkKeys {
			if err := flush(); err != nil {
				return types.Hash{}, err
			}
		}
	}

	// Final partial chunk.
	if err := flush(); err != nil {
		return types.Hash{}, err
	}

	log.Info("HPH: all chunks processed", "chunks", chunkIdx, "accounts", accCount, "storage", stoCount)

	// lastRoot is the root after the final Process call — that's the complete root.
	if lastRoot == nil {
		// Empty state (no accounts touched). Ask HPH for the current root.
		r, err := trie.RootHash()
		if err != nil {
			return types.Hash{}, err
		}
		lastRoot = r
	}

	var h types.Hash
	copy(h[:], lastRoot)
	return h, nil
}

// flushBranchesEveryChunks bounds in-memory branch growth during bulk
// rebuild. Quartered frequency (128 vs 32) keeps mem peak ~32 GB and
// cuts flush+cold-read overhead ~75% on hosts with >40 GB headroom.
// Flush in-memory branch store to MDBX every N chunks. Lower = lower
// peak memory but more MDBX commit overhead. 128 was the original (~big
// memory headroom) but 25M-state mainnet rebuild on Windows accumulates
// ~2 GB per chunk → would OOM a 128 GB box before chunk 128. 16 keeps
// peak under ~32 GB which is safe under page-cache pressure.
const flushBranchesEveryChunks = 16

// computeHPHFromPlainStateConcurrent is the 16-way parallel variant of
// computeHPHFromPlainState. Bulk-loads PlainState through
// ConcurrentMPTRootComputer.ProcessUpdates per chunk, which dispatches
// each chunk's keys across 16 worker goroutines (one per top nibble) via
// ParallelHashSort. Trie state persists across chunks (each chunk folds
// into the same root trie), so the final root is the cumulative state.
//
// Periodic mem flush: every flushBranchesEveryChunks chunks, mem.data is
// drained to MDBX MPTBranch table and the in-memory map is reset. Without
// this, mem.data grows monotonically over hundreds of chunks at full state
// scale (50M+ branches × 300 B ≈ 15-30 GB), causing slowdown + OOM.
//
// Auto-fallback: ConcurrentPatriciaHashed.Process flips
// updates.SetConcurrentCommitment(false) for the next call when the root
// shape isn't suitable (e.g. extension at root), and we preserve that
// across chunks. So early chunks may run sequentially until the trie
// has enough breadth, then later chunks run parallel.
func computeHPHFromPlainStateConcurrent(ctx context.Context, db kv.RwDB, logger log2.Logger) (types.Hash, error) {
	tmpdir, err := os.MkdirTemp("", "hph-bulk-conc-*")
	if err != nil {
		return types.Hash{}, err
	}
	defer os.RemoveAll(tmpdir)

	computer := commitment.NewPersistentConcurrentMPTRootComputer(db, tmpdir, logger)
	updates := libcommit.NewUpdates(libcommit.ModeDirect, tmpdir, libcommit.KeyToHexNibbleHash)
	updates.SetConcurrentCommitment(true)

	// Main RwTx + cursors. Recreated after each periodic flush+commit
	// (MDBX invalidates cursors on commit). Cursor positioning is driven
	// by `pendingResume`: when non-nil the next nextAccount() call will
	// Seek to it (and skip the exact match) instead of Next().
	var (
		tx            kv.RwTx
		accCur        kv.Cursor
		stoCur        kv.Cursor
		started       bool   // true after the first First()/Seek call
		pendingResume []byte // non-nil = Seek+skip-if-equal on next nextAccount call
	)
	openTxAndCursors := func() error {
		newTx, err := db.BeginRw(ctx)
		if err != nil {
			return err
		}
		ac, err := newTx.Cursor(modules.Account)
		if err != nil {
			newTx.Rollback()
			return err
		}
		sc, err := newTx.Cursor(modules.Storage)
		if err != nil {
			ac.Close()
			newTx.Rollback()
			return err
		}
		tx, accCur, stoCur = newTx, ac, sc
		// Wire the new tx into the computer for sequential fall-through reads.
		computer.SetStateReader(commitment.NewPlainStateMPTReader(newTx))
		computer.SetReadTx(newTx)
		return nil
	}
	closeTxAndCursors := func() {
		if accCur != nil {
			accCur.Close()
			accCur = nil
		}
		if stoCur != nil {
			stoCur.Close()
			stoCur = nil
		}
	}
	defer func() {
		closeTxAndCursors()
		if tx != nil {
			tx.Rollback()
		}
	}()

	// nextAccount returns the next Account entry, transparently handling
	// the three positioning cases:
	//   - very first call: cursor.First()
	//   - normal iteration: cursor.Next()
	//   - just reopened tx: Seek to pendingResume, skip if exact match
	nextAccount := func() ([]byte, []byte, error) {
		if !started {
			started = true
			k, v, err := accCur.First()
			if err != nil {
				return nil, nil, fmt.Errorf("accCur.First: %w", err)
			}
			return k, v, nil
		}
		if pendingResume != nil {
			seekKey := pendingResume
			pendingResume = nil
			k, v, err := accCur.Seek(seekKey)
			if err != nil {
				return nil, nil, fmt.Errorf("accCur.Seek(%x): %w", seekKey, err)
			}
			if k != nil && bytes.Equal(k, seekKey) {
				k, v, err = accCur.Next()
				if err != nil {
					return nil, nil, fmt.Errorf("accCur.Next after seek-skip: %w", err)
				}
				return k, v, nil
			}
			return k, v, nil
		}
		k, v, err := accCur.Next()
		if err != nil {
			return nil, nil, fmt.Errorf("accCur.Next: %w", err)
		}
		return k, v, nil
	}

	if err := openTxAndCursors(); err != nil {
		return types.Hash{}, err
	}

	var (
		accCount   int
		stoCount   int
		chunkKeys  int
		chunkIdx   int
		lastRoot   []byte
		lastAccKey []byte // last fully-processed account key (for reseek)
		lastLog    = time.Now()
		startTime  = time.Now()
		resumed    bool
	)

	// Resume from prior checkpoint if MDBX has one. Restores the HPH
	// root trie state and seeks past the last processed account so we
	// don't redo work after an interrupt or crash.
	if rkey, rstate, rErr := commitment.ReadBulkCheckpoint(tx); rErr == nil && rstate != nil {
		if err := computer.RestoreTrieState(rstate); err != nil {
			return types.Hash{}, fmt.Errorf("restore checkpoint trie state: %w", err)
		}
		if len(rkey) > 0 {
			pendingResume = append([]byte(nil), rkey...)
			lastAccKey = append([]byte(nil), rkey...)
		}
		resumed = true
		log.Info("HPH-conc resumed from checkpoint",
			"lastAccKey", fmt.Sprintf("%x", rkey),
			"trieStateBytes", len(rstate),
			"branchCount", computer.BranchCount())
	}
	_ = resumed

	maybeFlushMem := func() error {
		if chunkIdx == 0 || chunkIdx%flushBranchesEveryChunks != 0 {
			return nil
		}
		bcBefore := computer.BranchCount()
		tFlush := time.Now()
		closeTxAndCursors()
		// Reuse the existing RwTx for FlushBranches.
		if err := computer.FlushAndResetMem(tx); err != nil {
			tx.Rollback()
			tx = nil
			return fmt.Errorf("flush mem to MDBX: %w", err)
		}
		// Persist the resume checkpoint atomically with the branch flush
		// so a crash between Commit and the next chunk leaves a usable
		// (branches, lastAccKey, trieState) triple.
		trieState, tsErr := computer.EncodeTrieState()
		if tsErr != nil {
			tx.Rollback()
			tx = nil
			return fmt.Errorf("encode trie state: %w", tsErr)
		}
		if err := commitment.WriteBulkCheckpoint(tx, lastAccKey, trieState); err != nil {
			tx.Rollback()
			tx = nil
			return fmt.Errorf("write checkpoint: %w", err)
		}
		if err := tx.Commit(); err != nil {
			tx = nil
			return fmt.Errorf("commit flush: %w", err)
		}
		tx = nil
		if err := openTxAndCursors(); err != nil {
			return fmt.Errorf("reopen after flush: %w", err)
		}
		// Mark the reopened cursor to Seek to lastAccKey on the next
		// nextAccount call (and skip it, since it was already processed).
		pendingResume = append([]byte(nil), lastAccKey...)
		log.Info("HPH-conc checkpoint",
			"chunk", chunkIdx,
			"branchesFlushed", bcBefore,
			"trieStateBytes", len(trieState),
			"flushDur", time.Since(tFlush).Truncate(time.Millisecond))
		return nil
	}

	processChunk := func() error {
		if chunkKeys == 0 {
			return nil
		}
		chunkIdx++
		// Snapshot whether this chunk WILL run concurrently. Process
		// auto-flips the flag based on CanDoConcurrentNext after it
		// runs, so reading after is misleading — we want what the call
		// actually attempted.
		wasConcurrent := updates.IsConcurrentCommitment()
		tProc := time.Now()
		r, err := computer.ProcessUpdates(ctx, updates)
		if err != nil {
			return fmt.Errorf("hph-conc process chunk %d: %w", chunkIdx, err)
		}
		lastRoot = r
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		log.Info("HPH-conc chunk processed",
			"chunk", chunkIdx,
			"keys", chunkKeys,
			"accounts", accCount,
			"storage", stoCount,
			"chunkDur", time.Since(tProc).Truncate(time.Millisecond),
			"elapsed", time.Since(startTime).Truncate(time.Second),
			"allocMB", ms.Alloc/1e6,
			"branchCount", computer.BranchCount(),
			"concurrent", wasConcurrent)
		chunkKeys = 0
		updates.Reset()
		return maybeFlushMem()
	}

	// Single flat loop. nextAccount handles all positioning cases.
	for {
		k, v, err := nextAccount()
		if err != nil {
			return types.Hash{}, err
		}
		if k == nil {
			break // EOF
		}
		if len(k) != 20 {
			continue
		}
		updates.TouchPlainKey(string(k), v, updates.TouchAccount)
		accCount++
		chunkKeys++

		prefix := k
		for sk, sv, sErr := stoCur.Seek(prefix); sk != nil; sk, sv, sErr = stoCur.Next() {
			if sErr != nil {
				return types.Hash{}, sErr
			}
			if len(sk) < 20 || !bytesEqualPrefix(sk, prefix) {
				break
			}
			if len(sk) != 52 {
				continue
			}
			updates.TouchPlainKey(string(sk), sv, updates.TouchStorage)
			stoCount++
			chunkKeys++
		}

		// Save last fully-processed account key BEFORE any flush —
		// flush may invalidate k's underlying buffer.
		lastAccKey = append(lastAccKey[:0], k...)

		if time.Since(lastLog) > 15*time.Second {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			log.Info("HPH-conc ingest progress",
				"accounts", accCount,
				"storage", stoCount,
				"chunkKeys", chunkKeys,
				"elapsed", time.Since(startTime).Truncate(time.Second),
				"allocMB", ms.Alloc/1e6)
			lastLog = time.Now()
		}

		if chunkKeys >= hphChunkKeys {
			if err := processChunk(); err != nil {
				return types.Hash{}, err
			}
		}
	}

	if err := processChunk(); err != nil {
		return types.Hash{}, err
	}

	// Final flush + delete checkpoint atomically. After this commits
	// the rebuild is complete and a re-run from this datadir would
	// start clean (no stale checkpoint pointing into the past).
	if tx != nil {
		closeTxAndCursors()
		if err := computer.FlushAndResetMem(tx); err != nil {
			tx.Rollback()
			tx = nil
			return types.Hash{}, fmt.Errorf("final flush mem: %w", err)
		}
		if err := commitment.DeleteBulkCheckpoint(tx); err != nil {
			tx.Rollback()
			tx = nil
			return types.Hash{}, fmt.Errorf("delete checkpoint: %w", err)
		}
		if err := tx.Commit(); err != nil {
			tx = nil
			return types.Hash{}, fmt.Errorf("final commit: %w", err)
		}
		tx = nil
	}

	log.Info("HPH-conc: all chunks processed",
		"chunks", chunkIdx,
		"accounts", accCount,
		"storage", stoCount,
		"elapsed", time.Since(startTime).Truncate(time.Second),
		"branchCount", computer.BranchCount())

	if lastRoot == nil {
		r, err := computer.Trie().RootHash()
		if err != nil {
			return types.Hash{}, err
		}
		lastRoot = r
	}

	var h types.Hash
	copy(h[:], lastRoot)
	return h, nil
}

// bytesEqualPrefix reports whether k starts with prefix.
func bytesEqualPrefix(k, prefix []byte) bool {
	if len(k) < len(prefix) {
		return false
	}
	for i := range prefix {
		if k[i] != prefix[i] {
			return false
		}
	}
	return true
}
