// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// rebuild_state.go rebuilds PlainState from leaves journal.
// Accumulates in memory maps (last-write-wins dedup), flushes to MDBX
// when memory exceeds limit. No WriteMap — MDBX uses copy-on-write.

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

func RebuildState(ctx context.Context, db kv.RwDB, ancientDir string, endBlock uint64) error {
	t0 := time.Now()

	// Try leaves_journal (freezer table) first, then leaves (SegmentStore).
	// Open leaves table (batch-64 compressed, leaves.cidx + leaves.NNNN.cdat).
	leavesTbl, err := freezer.NewFreezerTable(ancientDir, "leaves", "c")
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

	// Note: ClearBucket on a full Storage table (40GB, billions of entries)
	// causes 185GB commit charge on Windows. Not safe to clear in-place.
	// Caller must delete mdbx.dat before running rebuild-state.
	// We verify the tables are empty to catch mistakes early.

	acctMap := make(map[types.Address][]byte, 1_000_000)
	storMap := make(map[string][]byte, 10_000_000)
	flushCount := 0
	lastLogTime := time.Now()

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

		accounts, storage, err := DecodeLeavesJournal(data)
		if err != nil {
			return fmt.Errorf("decode block %d: %w", blockNum, err)
		}

		for _, a := range accounts {
			if len(a.Value) == 0 {
				acctMap[a.Address] = nil
			} else {
				acctMap[a.Address] = a.Value
			}
		}
		for _, s := range storage {
			var key [52]byte
			copy(key[:20], s.Address[:])
			copy(key[20:], s.Slot[:])
			if len(s.Value) == 0 {
				storMap[string(key[:])] = nil
			} else {
				storMap[string(key[:])] = s.Value
			}
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
				"storage", len(storMap),
				"allocGB", fmt.Sprintf("%.1f", allocGB),
				"elapsed", time.Since(t0).Truncate(time.Second))
			lastLogTime = time.Now()

			if allocGB > float64(memLimitGB) {
				log.Info("Memory limit, flushing to MDBX...", "allocGB", fmt.Sprintf("%.1f", allocGB))
				if err := flushToMDBX(ctx, db, acctMap, storMap); err != nil {
					return err
				}
				acctMap = make(map[types.Address][]byte, 1_000_000)
				storMap = make(map[string][]byte, 10_000_000)
				runtime.GC()
				flushCount++
			}
		}
	}

	// Final flush.
	log.Info("Final flush", "accounts", len(acctMap), "storage", len(storMap), "totalFlushes", flushCount)
	if err := flushToMDBX(ctx, db, acctMap, storMap); err != nil {
		return err
	}
	acctMap = nil
	storMap = nil
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

func flushToMDBX(ctx context.Context, db kv.RwDB, acctMap map[types.Address][]byte, storMap map[string][]byte) error {
	if len(acctMap) == 0 && len(storMap) == 0 {
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

	// Sort storage keys.
	storKeys := make([]string, 0, len(storMap))
	for k := range storMap {
		storKeys = append(storKeys, k)
	}
	sort.Strings(storKeys)

	// Write sorted — MDBX Put is upsert, handles duplicates across flushes.
	tx, err := db.BeginRw(ctx)
	if err != nil {
		return err
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

	for i, k := range storKeys {
		v := storMap[k]
		if v == nil {
			tx.Delete(modules.Storage, []byte(k))
		} else {
			if err := tx.Put(modules.Storage, []byte(k), v); err != nil {
				tx.Rollback()
				return err
			}
		}
		if i > 0 && i%5_000_000 == 0 {
			log.Info("  writing storage", "progress", i, "total", len(storKeys))
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit flush: %w", err)
	}
	log.Info("Flush done",
		"accounts", len(acctKeys),
		"storage", len(storKeys),
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

	tx, err := db.BeginRw(ctx)
	if err != nil {
		return
	}
	if err := InitHashState(tx); err != nil {
		tx.Rollback()
		log.Warn("InitHashState failed", "err", err)
		return
	}
	tx.Commit()

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
