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
	if startBlock >= endBlock {
		return fmt.Errorf("start %d >= end %d", startBlock, endBlock)
	}
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

	for blockNum := startBlock; blockNum < endBlock; blockNum++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Forward replay: apply NEW values from the V2 changesets.
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
				// EVM fallback: revert the acctcs NEW values we just applied
				// (use OLD values) so MDBX state is at blockNum-1, then
				// execute blockNum via EVM.
				log.Warn("Corrupt storcs entry — falling back to EVM execution",
					"block", blockNum, "err", decErr)
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

	result, err := ProcessBlock(opts.ChainConfig, engine, header, body.Transactions, uncles, ibs, blockHashFunc, nil, writer)
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
		// newValueOf reads from the MDBX tx which now has the post-commit state.
		postReader := state.NewPlainStateReader(rwTx)
		stoCSBytes := EncodeStorageChanges(stoCS, func(addr types.Address, slot types.Hash) []byte {
			v, err := postReader.ReadAccountStorage(addr, 0, &slot)
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
			incarnation, err := postReader.ReadAccountIncarnation(addr)
			if err != nil {
				incarnation = 0
			}
			return state.EncodeAccountForHistory(a, false, incarnation)
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
