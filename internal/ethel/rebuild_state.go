// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"runtime"
	"sort"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
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
}

func RebuildState(ctx context.Context, db kv.RwDB, ancientDir string, endBlock uint64) error {
	return RebuildStateWith(ctx, db, ancientDir, endBlock, RebuildOptions{})
}

// RebuildStateWith is RebuildState with optional periodic verification.
func RebuildStateWith(ctx context.Context, db kv.RwDB, ancientDir string, endBlock uint64, opts RebuildOptions) error {
	t0 := time.Now()

	// Try leaves_journal (freezer table) first, then leaves (SegmentStore).
	// Open leaves table read-only (batch-64 compressed, leaves.cidx + leaves.NNNN.cdat).
	// Read-only is critical: rebuild-state must NEVER modify the source leaves
	// data. Without this, freezer.NewFreezerTable would truncate partial cidx
	// entries on open, mutating the file.
	leavesTbl, err := freezer.NewFreezerTableReadOnly(ancientDir, "leaves", "c")
	if err != nil {
		return fmt.Errorf("open leaves: %w", err)
	}
	defer leavesTbl.Close()
	leavesTbl.ForceBatchSize(freezer.BatchSize)
	leavesTbl.SetCompressed(true)
	items := leavesTbl.Items()
	if items == 0 {
		return fmt.Errorf("leaves table is empty (leaves.cidx has 0 entries)")
	}
	if endBlock == 0 || endBlock > items {
		endBlock = items
	}
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)
	log.Info("Rebuild PlainState from leaves",
		"blocks", endBlock,
		"memLimitGB", memLimitGB,
		"goAllocMB", m0.Alloc/1e6,
		"goSysMB", m0.Sys/1e6)
	log.Info("If Task Manager shows high Commit Size, check OTHER processes (reth, etc)")

	// Clear Account/Storage tables using ClearBucket (Drop + recreate DBI).
	// On current MDBX without WriteMap this is safe — only dirty pages use RAM.
	log.Info("Clearing Account/Storage tables...")
	for _, tbl := range []string{modules.Account, modules.Storage} {
		tx, err := db.BeginRw(ctx)
		if err != nil {
			return err
		}
		if err := tx.ClearBucket(tbl); err != nil {
			tx.Rollback()
			return fmt.Errorf("clear %s: %w", tbl, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit clear %s: %w", tbl, err)
		}
		log.Info("Table cleared", "table", tbl)
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
		if err := resetAndInitHashState(ctx, db); err != nil {
			return fmt.Errorf("init hash state at block %d: %w", blockNum, err)
		}
		tx2, err := db.BeginRw(ctx)
		if err != nil {
			return fmt.Errorf("verify begin tx2: %w", err)
		}
		root, err := CalcStateRoot(tx2)
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

	for blockNum := uint64(0); blockNum < endBlock; blockNum++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		data, err := leavesTbl.Retrieve(blockNum)
		if err != nil {
			return fmt.Errorf("read block %d: %w", blockNum, err)
		}
		if len(data) < 8 {
			continue
		}

		accounts, storage, wipes, err := DecodeLeavesJournal(data)
		if err != nil {
			return fmt.Errorf("decode block %d: %w", blockNum, err)
		}

		// Apply wipes: drop the entire per-address bucket from the in-memory
		// segment map AND record it in wipeSet so flushToMDBX also removes
		// any slots already persisted in MDBX from earlier segments.
		for _, addr := range wipes {
			delete(storMap, addr)
			wipeSet[addr] = struct{}{}
		}

		for _, a := range accounts {
			if len(a.Value) == 0 {
				acctMap[a.Address] = nil
			} else {
				acctMap[a.Address] = a.Value
			}
		}
		for _, s := range storage {
			inner, ok := storMap[s.Address]
			if !ok {
				inner = make(map[types.Hash][]byte, 8)
				storMap[s.Address] = inner
			}
			if len(s.Value) == 0 {
				inner[s.Slot] = nil
			} else {
				inner[s.Slot] = s.Value
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

	// Write progress.
	tx, err := db.BeginRw(ctx)
	if err != nil {
		return err
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], endBlock-1)
	tx.Put("DbInfo", []byte("ethel_progress"), buf[:])
	if err := tx.Commit(); err != nil {
		return err
	}

	log.Info("PlainState rebuild complete",
		"blocks", endBlock,
		"flushes", flushCount+1,
		"elapsed", time.Since(t0).Truncate(time.Second))
	return nil
}

// resetAndInitHashState clears HashedAccounts/HashedStorage then rebuilds
// them from PlainState. The clear is required because InitHashState
// short-circuits when HashedAccounts is non-empty, which would otherwise
// leave us computing roots from stale hashed state on a second verify.
func resetAndInitHashState(ctx context.Context, db kv.RwDB) error {
	tx, err := db.BeginRw(ctx)
	if err != nil {
		return err
	}
	if err := tx.ClearBucket(kv.HashedAccounts); err != nil {
		tx.Rollback()
		return fmt.Errorf("clear HashedAccounts: %w", err)
	}
	if err := tx.ClearBucket(kv.HashedStorage); err != nil {
		tx.Rollback()
		return fmt.Errorf("clear HashedStorage: %w", err)
	}
	if err := InitHashState(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
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

	for i, addr := range acctKeys {
		v := acctMap[addr]
		if v == nil {
			tx.Delete(modules.Account, addr[:])
		} else {
			if err := tx.Put(modules.Account, addr[:], v); err != nil {
				tx.Rollback()
				return err
			}
		}
		if i > 0 && i%1_000_000 == 0 {
			log.Info("  writing accounts", "progress", i, "total", len(acctKeys))
		}
	}

	for i, e := range storEntries {
		if e.value == nil {
			tx.Delete(modules.Storage, e.key)
		} else {
			if err := tx.Put(modules.Storage, e.key, e.value); err != nil {
				tx.Rollback()
				return err
			}
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

	if err := resetAndInitHashState(ctx, db); err != nil {
		log.Warn("resetAndInitHashState failed", "err", err)
		return
	}

	tx2, _ := db.BeginRw(ctx)
	root, err := CalcStateRoot(tx2)
	tx2.Rollback()
	if err != nil {
		log.Warn("CalcStateRoot failed", "err", err)
		return
	}
	if root == header.Root {
		log.Info("State root VERIFIED", "block", blockNum, "root", root.Hex(), "elapsed", time.Since(t0).Truncate(time.Second))
	} else {
		log.Error("State root MISMATCH", "block", blockNum, "computed", root.Hex(), "expected", header.Root.Hex())
	}
}
