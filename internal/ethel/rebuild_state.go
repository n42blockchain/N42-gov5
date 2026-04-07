// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// rebuild_state.go rebuilds PlainState from leaves journal files.
// Reads leaves in chunks, flushes to MDBX when memory exceeds limit.

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

const memoryLimitBytes = 100 * 1024 * 1024 * 1024 // 100 GB

// RebuildState reads leaves from a freezer table, accumulates in memory,
// flushes to MDBX when memory exceeds limit. No genesis.json needed —
// block 0 leaves contain all genesis accounts.
func RebuildState(ctx context.Context, db kv.RwDB, ancientDir string,
	inputFreezer *freezer.Freezer, endBlock uint64,
) error {
	t0 := time.Now()

	// Open leaves_journal table directly (bypass freezer frozen limit).
	journalTbl, err := freezer.NewFreezerTable(ancientDir, "leaves_journal", "c")
	if err != nil {
		return fmt.Errorf("open leaves_journal: %w", err)
	}
	defer journalTbl.Close()
	items := journalTbl.Items()
	if items == 0 {
		return fmt.Errorf("leaves_journal is empty")
	}
	if endBlock == 0 || endBlock > items {
		endBlock = items
	}
	log.Info("Reading leaves journal", "blocks", endBlock, "items", items)

	// Clear existing Account/Storage tables.
	log.Info("Clearing existing PlainState tables...")
	tx, err := db.BeginRw(ctx)
	if err != nil {
		return err
	}
	if err := tx.ClearBucket(modules.Account); err != nil {
		tx.Rollback()
		return fmt.Errorf("clear Account: %w", err)
	}
	if err := tx.ClearBucket(modules.Storage); err != nil {
		tx.Rollback()
		return fmt.Errorf("clear Storage: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit clear: %w", err)
	}

	// Accumulate leaves in memory, flush when memory exceeds limit.
	acctMap := make(map[types.Address][]byte, 1_000_000)
	storMap := make(map[string][]byte, 10_000_000)
	flushCount := 0
	lastLogTime := time.Now()

	for blockNum := uint64(0); blockNum < endBlock; blockNum++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		data, err := journalTbl.Retrieve(blockNum)
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
				acctMap[a.Address] = nil // mark deleted
			} else {
				acctMap[a.Address] = a.Value
			}
		}
		for _, s := range storage {
			var key [52]byte
			copy(key[:20], s.Address[:])
			copy(key[20:], s.Slot[:])
			k := string(key[:])
			if len(s.Value) == 0 {
				storMap[k] = nil // mark deleted
			} else {
				storMap[k] = s.Value
			}
		}

		// Periodic progress + memory check.
		if time.Since(lastLogTime) > 5*time.Second {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			allocGB := float64(m.Alloc) / 1e9
			sysGB := float64(m.Sys) / 1e9
			elapsed := time.Since(t0)
			pct := float64(blockNum) / float64(endBlock) * 100
			log.Info("Reading leaves",
				"block", blockNum,
				"pct", fmt.Sprintf("%.1f%%", pct),
				"accounts", len(acctMap),
				"storage", len(storMap),
				"allocGB", fmt.Sprintf("%.1f", allocGB),
				"sysGB", fmt.Sprintf("%.1f", sysGB),
				"elapsed", elapsed.Truncate(time.Second))
			lastLogTime = time.Now()

			// Flush if memory exceeds limit.
			if m.Alloc > memoryLimitBytes {
				log.Info("Memory limit reached, flushing to MDBX...",
					"allocGB", fmt.Sprintf("%.1f", allocGB),
					"accounts", len(acctMap),
					"storage", len(storMap))
				if err := flushMaps(ctx, db, acctMap, storMap); err != nil {
					return err
				}
				acctMap = make(map[types.Address][]byte, 1_000_000)
				storMap = make(map[string][]byte, 10_000_000)
				runtime.GC()
				flushCount++
				log.Info("Flush complete, continuing", "flushCount", flushCount)
			}
		}
	}

	// Final flush.
	log.Info("Final flush to MDBX...",
		"accounts", len(acctMap),
		"storage", len(storMap),
		"flushCount", flushCount)
	if err := flushMaps(ctx, db, acctMap, storMap); err != nil {
		return err
	}
	acctMap = nil
	storMap = nil
	runtime.GC()

	readElapsed := time.Since(t0)
	log.Info("PlainState rebuild complete",
		"blocks", endBlock,
		"flushes", flushCount+1,
		"elapsed", readElapsed.Truncate(time.Second))

	// Write progress marker.
	tx, err = db.BeginRw(ctx)
	if err != nil {
		return err
	}
	var progBuf [8]byte
	binary.BigEndian.PutUint64(progBuf[:], endBlock-1)
	if err := tx.Put("DbInfo", []byte("ethel_progress"), progBuf[:]); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	// Verify state root.
	log.Info("Computing state root for verification...")
	headerData, err := inputFreezer.Ancient(freezer.TableHeaders, endBlock-1)
	if err != nil {
		log.Warn("Cannot read header for verification", "block", endBlock-1, "err", err)
		return nil
	}
	header, err := DecodeGethHeader(headerData)
	if err != nil {
		log.Warn("Cannot decode header", "err", err)
		return nil
	}

	tx2, err := db.BeginRw(ctx)
	if err != nil {
		return err
	}
	if err := InitHashState(tx2); err != nil {
		tx2.Rollback()
		log.Warn("InitHashState failed", "err", err)
		return nil
	}
	if err := tx2.Commit(); err != nil {
		return err
	}

	tx3, err := db.BeginRw(ctx)
	if err != nil {
		return err
	}
	computedRoot, err := CalcStateRoot(tx3)
	tx3.Rollback()
	if err != nil {
		log.Warn("CalcStateRoot failed", "err", err)
	} else if computedRoot == header.Root {
		log.Info("State root VERIFIED",
			"block", endBlock-1,
			"root", header.Root.Hex(),
			"elapsed", time.Since(t0).Truncate(time.Second))
	} else {
		log.Error("State root MISMATCH",
			"block", endBlock-1,
			"computed", computedRoot.Hex(),
			"expected", header.Root.Hex())
	}

	return nil
}

// flushMaps writes account and storage maps to MDBX using sorted Put.
// Deleted entries (nil value) are written as Delete.
func flushMaps(ctx context.Context, db kv.RwDB, acctMap map[types.Address][]byte, storMap map[string][]byte) error {
	if len(acctMap) == 0 && len(storMap) == 0 {
		return nil
	}
	t0 := time.Now()

	// Sort account keys.
	acctKeys := make([]types.Address, 0, len(acctMap))
	for addr := range acctMap {
		acctKeys = append(acctKeys, addr)
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

	// Write to MDBX.
	tx, err := db.BeginRw(ctx)
	if err != nil {
		return err
	}

	for _, addr := range acctKeys {
		v := acctMap[addr]
		if v == nil {
			tx.Delete(modules.Account, addr[:])
		} else {
			if err := tx.Put(modules.Account, addr[:], v); err != nil {
				tx.Rollback()
				return fmt.Errorf("put account: %w", err)
			}
		}
	}

	for _, k := range storKeys {
		v := storMap[k]
		if v == nil {
			tx.Delete(modules.Storage, []byte(k))
		} else {
			if err := tx.Put(modules.Storage, []byte(k), v); err != nil {
				tx.Rollback()
				return fmt.Errorf("put storage: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit flush: %w", err)
	}

	log.Info("Flush written",
		"accounts", len(acctKeys),
		"storage", len(storKeys),
		"elapsed", time.Since(t0).Truncate(time.Second))
	return nil
}
