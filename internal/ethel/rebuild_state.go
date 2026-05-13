// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// rebuild_state.go — PlainState reconstruction from the leaves journal.
//
// RebuildState and RebuildStateWith open the freezer "leaves" table
// read-only, stream every per-block entry through DecodeJournal, and
// materialise PlainState into MDBX without re-executing any transactions.
// The in-memory accumulator is flushed whenever the Go heap exceeds
// memLimitGB. RebuildOptions.VerifyInterval enables periodic state-root
// verification against the input freezer's headers so the operator
// knows within N blocks if the journal is corrupt.

package ethel

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"sort"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

const memLimitGB = 100 // flush when Go heap exceeds this

// RebuildOptions controls intermediate verification during RebuildState.
type RebuildOptions struct {
	// VerifyInterval enables periodic state-root verification at every Nth
	// block. 0 disables (verify only at the end via VerifyRebuildRoot).
	// At each boundary the in-memory maps are flushed to MDBX, HashedState
	// is rebuilt from PlainState, and the computed state root is compared
	// against the header root from inputFreezer.
	VerifyInterval uint64
	// InputFreezer holds Geth-format ancient headers, used to look up the
	// expected state root at each verify boundary. Required if VerifyInterval
	// > 0.
	InputFreezer *freezer.Freezer
	// StartBlock — when > 0, leaves replay begins from this block
	// (inclusive) instead of 0 and the existing Account/Storage tables in
	// the destination MDBX are NOT cleared. Used to splice a fresh leaves
	// segment from a different freezer onto an already-rebuilt state, e.g.
	// to bridge over a corrupted hole by reusing a clean snapshot.
	StartBlock uint64
	// ChainConfig + GethFreezer enable EVM fallback: when a changeset
	// entry is corrupt, flush in-memory state to MDBX, execute the block
	// via EVM to produce the correct state transition, then continue.
	ChainConfig *params.ChainConfig
	GethFreezer *freezer.Freezer // input freezer for reading headers/bodies

	// EVMFromFallback — once any block triggers EVM fallback (e.g. corrupt
	// storcs), switch to EVM execution for EVERY subsequent block instead
	// of reading the (likely poisoned) freezer changesets. The freezer's
	// changesets produced by a prior ethexec run may encode the bugs that
	// originally caused the corruption; applying them after a fallback
	// produces forward state that silently diverges from geth. Default
	// false preserves the fast path for clean freezers.
	EVMFromFallback bool

	// PersistTrie — after reaching endBlock and flushing all changes to
	// Account/Storage, populate HashedAccounts/HashedStorage/TrieOfAccounts/
	// TrieOfStorage as well. Adds ~5-10 minutes at the 10M-block scale but
	// leaves the datadir fully HPH-bootstrapped so that subsequent
	// per-block verify (e.g. ethexec verify-incremental) can run in
	// O(dirty) instead of O(state).
	PersistTrie bool

	// WipesSidecarDir — optional path to a freezer dir containing a "wipes"
	// table produced by storcs-extract-wipes. Used to fill SELFDESTRUCT
	// pre-wipe entries that witness-replay's storcs structurally lacks
	// (its WitnessCapturingWriter has no MDBX state to enumerate slots
	// from on CreateContract). Per-block: any (addr, slot) in the sidecar
	// that's NOT already in this block's storcs is added with newVal=0
	// (a tombstone). Sidecar entries duplicating storcs entries are
	// silently skipped — storcs is authoritative for slots it knows about.
	WipesSidecarDir string
}

func RebuildState(ctx context.Context, db kv.RwDB, ancientDir string, endBlock uint64) error {
	return RebuildStateWith(ctx, db, ancientDir, endBlock, RebuildOptions{})
}

// RebuildStateWith is RebuildState with optional periodic verification.
//
// Forward replay reads per-block V2 changesets from acctcs and storcs and
// applies each entry's NEW value to PlainState. This is the "no-EVM
// forward generate" path — the EVM never runs, we just stream diffs.
// Backward unwind uses the same tables via reorg.go which reads OLD
// values from the same entries.
func RebuildStateWith(ctx context.Context, db kv.RwDB, ancientDir string, endBlock uint64, opts RebuildOptions) error {
	t0 := time.Now()

	// Open acctcs + storcs in read-only mode. Both tables share the
	// same batch-64 compressed format and must be aligned per block.
	acctTbl, err := freezer.NewFreezerTableReadOnly(ancientDir, "acctcs", "c")
	if err != nil {
		return fmt.Errorf("open acctcs: %w", err)
	}
	defer acctTbl.Close()
	acctTbl.ForceBatchSize(freezer.BatchSize)
	acctTbl.SetCompressed(true)

	stoTbl, err := freezer.NewFreezerTableReadOnly(ancientDir, "storcs", "c")
	if err != nil {
		return fmt.Errorf("open storcs: %w", err)
	}
	defer stoTbl.Close()
	stoTbl.ForceBatchSize(freezer.BatchSize)
	stoTbl.SetCompressed(true)

	// Optional wipes sidecar: parallel-indexed freezer table holding the
	// per-block SELFDESTRUCT pre-wipe entries that witness-replay's storcs
	// is structurally missing. When opened, we apply each block's sidecar
	// entries AFTER its storcs (sidecar fills gaps, never overrides).
	var wipesTbl *freezer.FreezerTable
	if opts.WipesSidecarDir != "" {
		wipesTbl, err = freezer.NewFreezerTableCompressedReadOnly(opts.WipesSidecarDir, freezer.TableWipes, "c")
		if err != nil {
			return fmt.Errorf("open wipes sidecar at %s: %w", opts.WipesSidecarDir, err)
		}
		defer wipesTbl.Close()
		wipesTbl.ForceBatchSize(freezer.BatchSize)
		log.Info("Wipes sidecar attached", "dir", opts.WipesSidecarDir, "items", wipesTbl.Items())
	}

	items := acctTbl.Items()
	if items == 0 {
		return fmt.Errorf("acctcs table is empty (acctcs.cidx has 0 entries)")
	}
	if stoItems := stoTbl.Items(); stoItems < items {
		items = stoItems
	}
	if endBlock == 0 || endBlock > items {
		endBlock = items
	}
	startBlock := opts.StartBlock
	if startBlock > endBlock {
		return fmt.Errorf("start %d > end %d", startBlock, endBlock)
	}
	// startBlock == endBlock is a legitimate "nothing to replay" case: used
	// when auto-resume progress == --end, so we can skip replay and go
	// straight to the optional --persist-trie BootstrapHPH step. The
	// for-loop below iterates [startBlock, endBlock) and becomes a no-op;
	// final flush is also empty; WriteProgress re-writes the same value
	// (idempotent); and PersistTrie runs normally.
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)
	log.Info("Rebuild PlainState from V2 changesets",
		"start", startBlock,
		"end", endBlock,
		"blocks", endBlock-startBlock,
		"memLimitGB", memLimitGB,
		"goAllocMB", m0.Alloc/1e6,
		"goSysMB", m0.Sys/1e6)
	log.Info("If Task Manager shows high Commit Size, check OTHER processes (reth, etc)")

	if startBlock == 0 {
		// Drop and recreate Account/Storage tables so their MDBX-level flags
		// (DUPSORT etc.) match the current TableCfg. ClearBucket keeps the
		// existing DBI flags, which can leave a stale DB in a state where
		// putDupSort fails with MDBX_INCOMPATIBLE on a non-DUPSORT DBI.
		log.Info("Recreating Account/Storage tables...")
		tx, err := db.BeginRw(ctx)
		if err != nil {
			return err
		}
		type bucketRecreator interface {
			ForceRecreateBucket(name string) error
		}
		rc, ok := tx.(bucketRecreator)
		if !ok {
			tx.Rollback()
			return fmt.Errorf("db does not support ForceRecreateBucket (got %T)", tx)
		}
		for _, tbl := range []string{modules.Account, modules.Storage} {
			if err := rc.ForceRecreateBucket(tbl); err != nil {
				tx.Rollback()
				return fmt.Errorf("recreate %s: %w", tbl, err)
			}
			log.Info("Table recreated", "table", tbl)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit recreate: %w", err)
		}
	} else {
		log.Info("Resume mode — preserving existing Account/Storage tables",
			"startBlock", startBlock)
	}

	acctMap := make(map[types.Address][]byte, 1_000_000)
	// storMap is keyed by address first, slot second. This makes wipes O(1)
	// (delete entire address bucket) instead of O(N) over all slots — a
	// critical perf fix once SELFDESTRUCT becomes common (block ~1M+).
	storMap := make(map[types.Address]map[types.Hash][]byte, 1_000_000)
	// wipeSet collects addresses whose ALL storage must be cleared from
	// MDBX at next flush. Without this, wipes that occur in segment N
	// only delete from segment N's in-memory map; storage written to MDBX
	// in segment N-1 stays orphaned and corrupts the next state root.
	wipeSet := make(map[types.Address]struct{})
	flushCount := 0
	lastLogTime := time.Now()

	// doVerify flushes the segment to MDBX, then rebuilds HashedAccounts/
	// HashedStorage and computes the state root via FlatDBTrieLoader.
	// Each call is O(current_state_size); use --verify 0 for fast rebuild.
	doVerify := func(blockNum uint64) error {
		log.Info("Verify boundary reached, flushing to MDBX",
			"block", blockNum, "accounts", len(acctMap),
			"addrs", len(storMap), "wipes", len(wipeSet))
		if err := flushToMDBX(ctx, db, acctMap, storMap, wipeSet); err != nil {
			return fmt.Errorf("flush before verify: %w", err)
		}
		acctMap = make(map[types.Address][]byte, 1_000_000)
		storMap = make(map[types.Address]map[types.Hash][]byte, 1_000_000)
		wipeSet = make(map[types.Address]struct{})
		runtime.GC()
		flushCount++

		log.Info("Computing state root", "block", blockNum)
		tx2, err := db.BeginRw(ctx)
		if err != nil {
			return fmt.Errorf("verify begin tx2: %w", err)
		}
		// Use the streaming FlatDBTrieLoader path (FullStateRootVerify)
		// rather than the in-memory MPT (VerifyStateRoot) — the latter
		// builds account+storage tries entirely on the heap and OOMs
		// past ~10M blocks (32M+ accounts). FullStateRootVerify clears
		// + repopulates HashedAccounts/HashedStorage and TrieOf* via
		// streaming, peak ~5-10 GB regardless of state size.
		root, err := FullStateRootVerify(tx2)
		tx2.Rollback()
		if err != nil {
			return fmt.Errorf("calc state root at block %d: %w", blockNum, err)
		}
		// Read the header at blockNum from the input freezer.
		hdrData, err := opts.InputFreezer.Ancient(freezer.TableHeaders, blockNum)
		if err != nil {
			log.Warn("Cannot read header for verify", "block", blockNum, "err", err)
			return nil
		}
		hdr, err := DecodeGethHeader(hdrData)
		if err != nil {
			log.Warn("Cannot decode header for verify", "block", blockNum, "err", err)
			return nil
		}
		if root == hdr.Root {
			log.Info("STATE ROOT VERIFIED",
				"block", blockNum,
				"root", root.Hex(),
				"elapsed", time.Since(t0).Truncate(time.Second))
		} else {
			log.Error("STATE ROOT MISMATCH",
				"block", blockNum,
				"computed", root.Hex(),
				"expected", hdr.Root.Hex())
			return fmt.Errorf("state root mismatch at block %d", blockNum)
		}
		return nil
	}

	// evmOnly flips once a fallback fires with opts.EVMFromFallback set,
	// making every subsequent block run through the EVM instead of trusting
	// the remaining freezer changesets (which were written by the same run
	// whose corruption triggered the fallback).
	evmOnly := false

	for blockNum := startBlock; blockNum < endBlock; blockNum++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if evmOnly {
			// Post-fallback EVM mode: flush any pending in-memory state
			// (usually empty after the fallback reset) so the EVM reader
			// sees up-to-date MDBX, then execute this block via EVM.
			if len(acctMap) > 0 || len(storMap) > 0 || len(wipeSet) > 0 {
				if err := flushToMDBX(ctx, db, acctMap, storMap, wipeSet); err != nil {
					return fmt.Errorf("flush before EVM at %d: %w", blockNum, err)
				}
				acctMap = make(map[types.Address][]byte, 1_000_000)
				storMap = make(map[types.Address]map[types.Hash][]byte, 1_000_000)
				wipeSet = make(map[types.Address]struct{})
				flushCount++
			}
			if err := rebuildEVMFallback(ctx, db, opts, blockNum); err != nil {
				return fmt.Errorf("EVM at block %d: %w", blockNum, err)
			}

			// Verify boundary in EVM-only mode — no maps to flush.
			if opts.VerifyInterval > 0 && opts.InputFreezer != nil &&
				(blockNum+1)%opts.VerifyInterval == 0 {
				if err := doVerify(blockNum); err != nil {
					return err
				}
				lastLogTime = time.Now()
			}

			if time.Since(lastLogTime) > 5*time.Second {
				pct := float64(blockNum) / float64(endBlock) * 100
				log.Info("EVM-only replay",
					"block", blockNum,
					"pct", fmt.Sprintf("%.1f%%", pct),
					"elapsed", time.Since(t0).Truncate(time.Second))
				lastLogTime = time.Now()
			}
			continue
		}

		// Forward replay: apply NEW values from the V2 changesets.
		// Wipes-sidecar runs FIRST: clearing storMap[addr] for each wiped
		// address drops slots accumulated by prior blocks in this flush
		// interval (e.g. block N wrote B[s1]=V, block N+5 SELFDESTRUCTs
		// B). Same-block SSTOREs that happen after the wipe — like
		// SELFDESTRUCT+CREATE2+SSTORE — are added back by the storcs
		// parser below, so they survive cleanly.
		if err := applyWipesSidecar(wipesTbl, blockNum, wipeSet, storMap); err != nil {
			return err
		}

		accData, err := acctTbl.Retrieve(blockNum)
		if err != nil {
			return fmt.Errorf("read acctcs block %d: %w", blockNum, err)
		}
		if len(accData) > 0 {
			entries, err := DecodeAccountChanges(accData)
			if err != nil {
				return fmt.Errorf("decode acctcs block %d: %w", blockNum, err)
			}
			for _, e := range entries {
				if len(e.NewValue) == 0 {
					acctMap[e.Address] = nil
				} else {
					acctMap[e.Address] = e.NewValue
				}
			}
		}

		stoData, err := stoTbl.Retrieve(blockNum)
		if err != nil {
			return fmt.Errorf("read storcs block %d: %w", blockNum, err)
		}
		if len(stoData) > 0 {
			entries, decErr := DecodeStorageChanges(stoData)
			if decErr != nil {
				if opts.ChainConfig == nil || opts.GethFreezer == nil {
					return fmt.Errorf("decode storcs block %d: %w (pass --ancient to enable EVM fallback)", blockNum, decErr)
				}
				// EVM fallback: revert this block's account changes
				// (use OLD values) so the in-memory state is at
				// blockNum-1, flush everything to MDBX, then execute
				// blockNum via EVM against the clean MDBX state.
				log.Warn("Corrupt storcs entry — falling back to EVM execution",
					"block", blockNum, "err", decErr,
					"batchAccts", len(acctMap),
					"batchAddrs", len(storMap))
				if len(accData) > 0 {
					revert, _ := DecodeAccountChanges(accData)
					for _, e := range revert {
						if len(e.OldValue) == 0 {
							acctMap[e.Address] = nil
						} else {
							acctMap[e.Address] = e.OldValue
						}
					}
				}
				if err := flushToMDBX(ctx, db, acctMap, storMap, wipeSet); err != nil {
					return fmt.Errorf("flush before EVM fallback at %d: %w", blockNum, err)
				}
				acctMap = make(map[types.Address][]byte, 1_000_000)
				storMap = make(map[types.Address]map[types.Hash][]byte, 1_000_000)
				wipeSet = make(map[types.Address]struct{})
				flushCount++
				if err := rebuildEVMFallback(ctx, db, opts, blockNum); err != nil {
					return fmt.Errorf("EVM fallback at block %d: %w", blockNum, err)
				}
				if opts.EVMFromFallback {
					log.Warn("EVM-only mode engaged — remaining blocks will bypass freezer changesets",
						"fromBlock", blockNum+1)
					evmOnly = true
				}
				continue
			}
			for _, e := range entries {
				var addr types.Address
				var slot types.Hash
				copy(addr[:], e.CompositeKey[:20])
				copy(slot[:], e.CompositeKey[20:])
				inner, ok := storMap[addr]
				if !ok {
					inner = make(map[types.Hash][]byte, 8)
					storMap[addr] = inner
				}
				// newLen=0 covers both SSTORE-to-zero and the implicit
				// SELFDESTRUCT wipe — per-slot tombstone drops the row
				// on next flush. Unlike the legacy wipes-list path, no
				// address-level prefix delete is needed: CreateContract's
				// pre-wipe enumeration means every wiped slot has its
				// own entry here.
				inner[slot] = e.NewValue
			}
		}

		// Periodic verify: at blockNum where (blockNum+1) % VerifyInterval == 0
		// (i.e., we just applied the last block of an interval).
		if opts.VerifyInterval > 0 && opts.InputFreezer != nil &&
			(blockNum+1)%opts.VerifyInterval == 0 {
			if err := doVerify(blockNum); err != nil {
				return err
			}
			lastLogTime = time.Now()
		}

		if time.Since(lastLogTime) > 5*time.Second {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			allocGB := float64(m.Alloc) / 1e9
			pct := float64(blockNum) / float64(endBlock) * 100
			log.Info("Reading leaves",
				"block", blockNum,
				"pct", fmt.Sprintf("%.1f%%", pct),
				"accounts", len(acctMap),
				"addrs", len(storMap),
				"allocGB", fmt.Sprintf("%.1f", allocGB),
				"elapsed", time.Since(t0).Truncate(time.Second))
			lastLogTime = time.Now()

			if allocGB > float64(memLimitGB) {
				log.Info("Memory limit, flushing to MDBX...", "allocGB", fmt.Sprintf("%.1f", allocGB))
				if err := flushToMDBX(ctx, db, acctMap, storMap, wipeSet); err != nil {
					return err
				}
				acctMap = make(map[types.Address][]byte, 1_000_000)
				storMap = make(map[types.Address]map[types.Hash][]byte, 1_000_000)
				wipeSet = make(map[types.Address]struct{})
				runtime.GC()
				flushCount++
			}
		}
	}

	// Final flush.
	log.Info("Final flush", "accounts", len(acctMap), "addrs", len(storMap),
		"wipes", len(wipeSet), "totalFlushes", flushCount)
	if err := flushToMDBX(ctx, db, acctMap, storMap, wipeSet); err != nil {
		return err
	}
	acctMap = nil
	storMap = nil
	wipeSet = nil
	runtime.GC()

	// Write progress under BOTH marker keys so subsequent forward replay
	// (which reads SyncStageProgress/ethel-last-block) and resume
	// (which reads DbInfo/ethel_progress) see the same checkpoint.
	// Using only DbInfo here used to silently leave forward replay
	// thinking the datadir is at block 0 — observed twice on
	// d:\N42-eth1177 and d:\N42-eth1456 — and ethexec then attempted
	// a "block 0 genesis snapshot output" of the existing 169M-account
	// PlainState, overflowing the snapshot's uint16 account counter.
	tx, err := db.BeginRw(ctx)
	if err != nil {
		return err
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], endBlock-1)
	if err := tx.Put("DbInfo", []byte("ethel_progress"), buf[:]); err != nil {
		tx.Rollback()
		return err
	}
	if err := WriteProgress(tx, endBlock-1); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	log.Info("PlainState rebuild complete",
		"blocks", endBlock,
		"flushes", flushCount+1,
		"elapsed", time.Since(t0).Truncate(time.Second))

	// Optional: bootstrap HPH tables so the datadir is ready for
	// per-block incremental root computation.
	if opts.PersistTrie {
		log.Info("Bootstrapping HPH tables (HashedAccounts/HashedStorage/TrieOfAccounts/TrieOfStorage)...")
		tBootstrap := time.Now()
		// Use the batched variant so we don't overflow MDBX's dirty-page
		// pool on 12M+ block states (HashedStorage alone is 100M+ puts
		// into a DupSort table, well past any reasonable single-tx limit).
		root, err := BootstrapHPHBatched(ctx, db, 0, 0)
		if err != nil {
			return fmt.Errorf("hph bootstrap: %w", err)
		}
		log.Info("HPH bootstrap complete",
			"root", root.Hex(),
			"elapsed", time.Since(tBootstrap).Truncate(time.Second))

		// Verify bootstrap root against header.
		if opts.InputFreezer != nil {
			hdrData, err := opts.InputFreezer.Ancient(freezer.TableHeaders, endBlock-1)
			if err == nil {
				if hdr, derr := DecodeGethHeader(hdrData); derr == nil {
					if root == hdr.Root {
						log.Info("HPH bootstrap root VERIFIED against header",
							"block", endBlock-1, "root", root.Hex())
					} else {
						log.Error("HPH bootstrap root MISMATCH vs header",
							"block", endBlock-1,
							"computed", root.Hex(),
							"expected", hdr.Root.Hex())
						return fmt.Errorf("hph bootstrap root mismatch at block %d", endBlock-1)
					}
				}
			}
		}
	}
	return nil
}

func flushToMDBX(ctx context.Context, db kv.RwDB, acctMap map[types.Address][]byte, storMap map[types.Address]map[types.Hash][]byte, wipeSet map[types.Address]struct{}) error {
	totalSlots := 0
	for _, slots := range storMap {
		totalSlots += len(slots)
	}
	if len(acctMap) == 0 && totalSlots == 0 && len(wipeSet) == 0 {
		return nil
	}
	t0 := time.Now()

	// Sort account keys.
	acctKeys := make([]types.Address, 0, len(acctMap))
	for a := range acctMap {
		acctKeys = append(acctKeys, a)
	}
	sort.Slice(acctKeys, func(i, j int) bool {
		return bytes.Compare(acctKeys[i][:], acctKeys[j][:]) < 0
	})

	// Build sorted composite-key list for storage.
	type stoEntry struct {
		key   []byte
		value []byte
	}
	storEntries := make([]stoEntry, 0, totalSlots)
	for addr, slots := range storMap {
		for slot, value := range slots {
			compositeKey := make([]byte, 52)
			copy(compositeKey[:20], addr[:])
			copy(compositeKey[20:], slot[:])
			storEntries = append(storEntries, stoEntry{key: compositeKey, value: value})
		}
	}
	sort.Slice(storEntries, func(i, j int) bool {
		return bytes.Compare(storEntries[i].key, storEntries[j].key) < 0
	})

	// Write sorted — MDBX Put is upsert, handles duplicates across flushes.
	tx, err := db.BeginRw(ctx)
	if err != nil {
		return err
	}

	// Phase 0: process wipes. For each wiped address, delete every storage
	// slot in MDBX whose key starts with that address. The newly written
	// slots in storMap (from CREATE-after-SELFDESTRUCT or contract reuse)
	// are written in phase 2 below and overwrite the deletions cleanly.
	//
	// Two-pass design: first collect all keys to delete with one cursor
	// (closed cleanly between addresses), then delete them outside the
	// cursor loop. Mixing tx.Delete with an open cursor on the same table
	// invalidates the cursor's iteration state and can silently miss
	// subsequent slots — exactly the bug that produced wrong roots at 6M+.
	if len(wipeSet) > 0 {
		// Sort wiped addresses for sequential cursor seeks (faster on B+tree).
		wipeAddrs := make([]types.Address, 0, len(wipeSet))
		for addr := range wipeSet {
			wipeAddrs = append(wipeAddrs, addr)
		}
		sort.Slice(wipeAddrs, func(i, j int) bool {
			return bytes.Compare(wipeAddrs[i][:], wipeAddrs[j][:]) < 0
		})

		// Pass 1: collect keys to delete.
		var allKeysToDelete [][]byte
		cursor, err := tx.Cursor(modules.Storage)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("open storage cursor for wipe: %w", err)
		}
		for _, addr := range wipeAddrs {
			prefix := addr[:]
			for k, _, err := cursor.Seek(prefix); k != nil; k, _, err = cursor.Next() {
				if err != nil {
					cursor.Close()
					tx.Rollback()
					return err
				}
				if len(k) < 20 || !bytes.Equal(k[:20], prefix) {
					break
				}
				allKeysToDelete = append(allKeysToDelete, append([]byte{}, k...))
			}
		}
		cursor.Close()

		// Pass 2: delete all collected keys (cursor is closed, no invalidation).
		for _, k := range allKeysToDelete {
			if err := tx.Delete(modules.Storage, k); err != nil {
				tx.Rollback()
				return err
			}
		}
		if len(allKeysToDelete) > 0 {
			log.Info("  wiped storage entries from MDBX",
				"addrs", len(wipeSet), "slots", len(allKeysToDelete))
		}
	}

	// MDBX caps dirty pages per transaction (~2M pages × 16KB ≈ 32GB);
	// a flush of 10M+ accounts or 100M+ storage rows in a single tx
	// will overflow and surface as MDBX_BAD_TXN mid-Put. Commit every
	// flushBatchOps and reopen so each tx stays well under the cap.
	// Ordering within (accounts, storage) is preserved across commits
	// because we iterate sorted slices; the wipe phase above already
	// committed separately via its own 2-pass cursor design.
	const flushBatchOps = 500_000
	ops := 0
	maybeRotate := func() error {
		if ops < flushBatchOps {
			return nil
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit flush batch: %w", err)
		}
		var err error
		tx, err = db.BeginRw(ctx)
		if err != nil {
			return fmt.Errorf("reopen flush tx: %w", err)
		}
		ops = 0
		return nil
	}

	for i, addr := range acctKeys {
		v := acctMap[addr]
		if v == nil {
			if err := tx.Delete(modules.Account, addr[:]); err != nil {
				tx.Rollback()
				return err
			}
		} else {
			if err := tx.Put(modules.Account, addr[:], v); err != nil {
				tx.Rollback()
				return err
			}
		}
		ops++
		if err := maybeRotate(); err != nil {
			return err
		}
		if i > 0 && i%1_000_000 == 0 {
			log.Info("  writing accounts", "progress", i, "total", len(acctKeys))
		}
	}

	for i, e := range storEntries {
		if e.value == nil {
			if err := tx.Delete(modules.Storage, e.key); err != nil {
				tx.Rollback()
				return err
			}
		} else {
			if err := tx.Put(modules.Storage, e.key, e.value); err != nil {
				tx.Rollback()
				return err
			}
		}
		ops++
		if err := maybeRotate(); err != nil {
			return err
		}
		if i > 0 && i%5_000_000 == 0 {
			log.Info("  writing storage", "progress", i, "total", len(storEntries))
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit flush: %w", err)
	}
	log.Info("Flush done",
		"accounts", len(acctKeys),
		"storage", len(storEntries),
		"elapsed", time.Since(t0).Truncate(time.Second))
	return nil
}

// deleteBatch deletes up to limit keys from a table in one transaction.
// Returns the number of keys deleted. Call repeatedly until 0.
func deleteBatch(ctx context.Context, db kv.RwDB, table string, limit int) (int, error) {
	tx, err := db.BeginRw(ctx)
	if err != nil {
		return 0, err
	}
	cursor, err := tx.RwCursor(table)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	deleted := 0
	for k, _, err := cursor.First(); k != nil && deleted < limit; k, _, err = cursor.Next() {
		if err != nil {
			cursor.Close()
			tx.Rollback()
			return 0, err
		}
		if err := cursor.DeleteCurrent(); err != nil {
			cursor.Close()
			tx.Rollback()
			return 0, err
		}
		deleted++
	}
	cursor.Close()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}

// VerifyRebuildRoot verifies rebuilt PlainState against header state root.
func VerifyRebuildRoot(ctx context.Context, db kv.RwDB, inputFreezer *freezer.Freezer, endBlock uint64) {
	if endBlock == 0 {
		return
	}
	blockNum := endBlock - 1
	t0 := time.Now()
	log.Info("Verifying state root...", "block", blockNum)

	headerData, err := inputFreezer.Ancient(freezer.TableHeaders, blockNum)
	if err != nil {
		log.Warn("Cannot read header", "err", err)
		return
	}
	header, err := DecodeGethHeader(headerData)
	if err != nil {
		log.Warn("Cannot decode header", "err", err)
		return
	}

	tx2, _ := db.BeginRw(ctx)
	// Streaming MPT verify (FullStateRootVerify → FlatDBTrieLoader),
	// not in-memory MPT (VerifyStateRoot). The in-memory path OOMs at
	// 10M+ blocks per the comment in executor.go:verifyBlockCap; this
	// path is bounded ~5-10 GB regardless of state size.
	root, err := FullStateRootVerify(tx2)
	tx2.Rollback()
	if err != nil {
		log.Warn("FullStateRootVerify failed", "err", err)
		return
	}
	if root == header.Root {
		log.Info("State root VERIFIED", "block", blockNum, "root", root.Hex(), "elapsed", time.Since(t0).Truncate(time.Second))
	} else {
		log.Error("State root MISMATCH", "block", blockNum, "computed", root.Hex(), "expected", header.Root.Hex())
	}
}

// rebuildEVMFallback executes a single block via EVM against the current
// MDBX PlainState and commits the resulting state changes. Used when a
// changeset entry is corrupt and cannot be decoded.
func rebuildEVMFallback(ctx context.Context, db kv.RwDB, opts RebuildOptions, blockNum uint64) error {
	headerData, err := opts.GethFreezer.Ancient(freezer.TableHeaders, blockNum)
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	header, err := DecodeGethHeader(headerData)
	if err != nil {
		return fmt.Errorf("decode header: %w", err)
	}
	bodyData, err := opts.GethFreezer.Ancient(freezer.TableBodies, blockNum)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	body, err := DecodeGethBody(bodyData)
	if err != nil {
		return fmt.Errorf("decode body: %w", err)
	}

	var uncles []block.IHeader
	for _, u := range body.Uncles {
		uncles = append(uncles, u)
	}

	engine := NewEthReplayEngine(opts.ChainConfig)

	rwTx, err := db.BeginRw(ctx)
	if err != nil {
		return err
	}
	defer rwTx.Rollback()

	reader := state.NewPlainStateReader(rwTx)
	// Use rwTx as both the put/del target and changeset DB.
	writer := state.NewPlainStateWriter(rwTx, rwTx, blockNum)
	ibs := state.New(reader)

	blockHashFunc := func(n uint64) types.Hash {
		if n >= blockNum {
			return types.Hash{}
		}
		hData, err := opts.GethFreezer.Ancient(freezer.TableHeaders, n)
		if err != nil {
			return types.Hash{}
		}
		h, err := DecodeGethHeader(hData)
		if err != nil {
			return types.Hash{}
		}
		return h.Hash()
	}

	result, err := ProcessBlock(opts.ChainConfig, engine, header, body.Transactions, uncles, body.Withdrawals, ibs, blockHashFunc, nil, writer)
	if err != nil {
		return fmt.Errorf("execute block: %w", err)
	}

	rules := opts.ChainConfig.Rules(header.Number.Uint64())
	if err := ibs.CommitBlock(rules, writer); err != nil {
		return fmt.Errorf("commit block: %w", err)
	}

	// Generate and save the correct changeset for later freezer splicing.
	csw := writer.ChangeSetWriter()
	if csw != nil {
		stoCS, _ := csw.GetStorageChanges()
		accCS, _ := csw.GetAccountChanges()
		log.Info("EVM fallback changeset",
			"stoChanges", stoCS.Len(),
			"accChanges", accCS.Len())
		// newValueOf reads from the MDBX tx which now has the post-commit state.
		postReader := state.NewPlainStateReader(rwTx)
		stoCSBytes := EncodeStorageChanges(stoCS, func(addr types.Address, slot types.Hash) []byte {
			v, err := postReader.ReadAccountStorage(addr, &slot)
			if err != nil {
				return nil
			}
			return v
		})
		accCSBytes := EncodeAccountChanges(accCS, func(addr types.Address) []byte {
			a, err := postReader.ReadAccountData(addr)
			if err != nil || a == nil {
				return nil
			}
			// Reth-style: full V2 (with CodeHash, no hidden incarnation tag).
			return a.MarshalV2()
		})
		patchFile := fmt.Sprintf("storcs_patch_%d.bin", blockNum)
		if err := os.WriteFile(patchFile, stoCSBytes, 0644); err != nil {
			log.Warn("Failed to save storcs patch", "file", patchFile, "err", err)
		} else {
			log.Info("Saved correct storcs patch", "file", patchFile, "bytes", len(stoCSBytes))
		}
		patchFile2 := fmt.Sprintf("acctcs_patch_%d.bin", blockNum)
		if err := os.WriteFile(patchFile2, accCSBytes, 0644); err != nil {
			log.Warn("Failed to save acctcs patch", "file", patchFile2, "err", err)
		} else {
			log.Info("Saved correct acctcs patch", "file", patchFile2, "bytes", len(accCSBytes))
		}
	}

	if err := rwTx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	log.Info("EVM fallback complete", "block", blockNum, "gasUsed", result.GasUsed, "txs", len(body.Transactions))
	return nil
}

// applyWipesSidecar backfills SELFDESTRUCT pre-wipe addresses that the
// upstream storcs is missing (witness-replay's WitnessCapturingWriter has
// no MDBX state to enumerate slots from on CreateContract; only the slots
// the EVM actually read in the same block land in storcs).
//
// The sidecar payload per block is a packed `addr20 × N` stream (length
// always a multiple of 20). For each addr we add to wipeSet — the
// existing flush path already does an MDBX prefix-delete on Storage for
// every address in wipeSet, which is O(slots_of_addr) and cache-friendly
// because Storage is keyed by addr+slot (all slots for one addr are
// physically contiguous in the B-tree).
//
// This format replaces the older per-slot-tombstone storcs-encoded
// sidecar (produced by cmd/storcs-extract-wipes): it's roughly 3000×
// smaller because the slot enumeration happens at apply time via MDBX
// prefix scan instead of being pre-materialised on disk. Produced by
// cmd/acctcs-extract-wipes from a known-good main-archive acctcs+storcs
// (signal: acctcs newVal=empty ∪ storcs bulk-newVal=empty).
//
// Must be called BEFORE the per-block acctcs+storcs parser. Two effects:
//
//  1. wipeSet[addr] = {} so flushToMDBX phase 0 prefix-deletes every row
//     keyed by addr from the MDBX Storage table at the next flush.
//
//  2. delete(storMap, addr) drops any in-memory slot entries this flush
//     interval accumulated from prior blocks. Without this, a SSTORE in
//     block N (storMap[addr][slot] = V) followed by SELFDESTRUCT in
//     block N+K with no flush in between would write V back to MDBX in
//     flush phase 1 AFTER phase 0 wiped MDBX, leaving a ghost row.
//     Same-block SSTOREs that follow the wipe (path 2: SELFDESTRUCT +
//     CREATE2 + SSTORE) re-populate storMap[addr] cleanly because the
//     storcs parser runs AFTER this call.
//
// No-op when wipesTbl is nil or blockNum is past the sidecar's tail.
// Fails loudly if the payload length is not a multiple of 20 — that
// indicates an old-format sidecar pointed at by --wipes-sidecar; the
// caller must regenerate via cmd/acctcs-extract-wipes.
func applyWipesSidecar(wipesTbl *freezer.FreezerTable, blockNum uint64, wipeSet map[types.Address]struct{}, storMap map[types.Address]map[types.Hash][]byte) error {
	if wipesTbl == nil || blockNum >= wipesTbl.Items() {
		return nil
	}
	data, err := wipesTbl.Retrieve(blockNum)
	if err != nil {
		return fmt.Errorf("read wipes block %d: %w", blockNum, err)
	}
	if len(data) == 0 {
		return nil
	}
	if len(data)%20 != 0 {
		return fmt.Errorf("wipes block %d: payload len=%d is not a multiple of 20 "+
			"(addr-only format expected — regenerate via cmd/acctcs-extract-wipes)",
			blockNum, len(data))
	}
	for off := 0; off < len(data); off += 20 {
		var addr types.Address
		copy(addr[:], data[off:off+20])
		wipeSet[addr] = struct{}{}
		delete(storMap, addr)
	}
	return nil
}
