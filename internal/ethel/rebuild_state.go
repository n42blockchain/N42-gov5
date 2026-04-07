// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// rebuild_state.go rebuilds PlainState from leaves journal.
// Directly Put to MDBX (upsert) — no in-memory map, zero OOM risk.
// Commits every N blocks to keep MDBX dirty pages bounded.

package ethel

import (
	"context"
	"encoding/binary"
	"fmt"
	"runtime"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// RebuildState reads leaves from leaves_journal and writes PlainState
// directly to MDBX. Each leaf is a Put (upsert) — MDBX handles dedup.
// Block 0 contains genesis accounts, no genesis.json needed.
func RebuildState(ctx context.Context, db kv.RwDB, ancientDir string,
	inputFreezer *freezer.Freezer, endBlock uint64,
) error {
	t0 := time.Now()

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
	log.Info("Rebuild PlainState from leaves", "blocks", endBlock, "items", items)

	// Clear existing PlainState.
	log.Info("Clearing Account/Storage tables...")
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

	// Replay leaves directly to MDBX.
	const commitInterval = 100_000 // commit every 100K blocks
	tx, err = db.BeginRw(ctx)
	if err != nil {
		return err
	}

	var totalAcct, totalStor uint64
	lastLogTime := time.Now()

	for blockNum := uint64(0); blockNum < endBlock; blockNum++ {
		if ctx.Err() != nil {
			tx.Rollback()
			return ctx.Err()
		}

		data, err := journalTbl.Retrieve(blockNum)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("read block %d: %w", blockNum, err)
		}
		if len(data) < 8 {
			continue
		}

		accounts, storage, err := DecodeLeavesJournal(data)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("decode block %d: %w", blockNum, err)
		}

		for _, a := range accounts {
			if len(a.Value) == 0 {
				tx.Delete(modules.Account, a.Address[:])
			} else {
				if err := tx.Put(modules.Account, a.Address[:], a.Value); err != nil {
					tx.Rollback()
					return fmt.Errorf("put account block %d: %w", blockNum, err)
				}
			}
			totalAcct++
		}

		for _, s := range storage {
			key := make([]byte, 52)
			copy(key[:20], s.Address[:])
			copy(key[20:], s.Slot[:])
			if len(s.Value) == 0 {
				tx.Delete(modules.Storage, key)
			} else {
				if err := tx.Put(modules.Storage, key, s.Value); err != nil {
					tx.Rollback()
					return fmt.Errorf("put storage block %d: %w", blockNum, err)
				}
			}
			totalStor++
		}

		// Periodic commit to keep dirty pages bounded.
		if blockNum > 0 && blockNum%commitInterval == 0 {
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit at block %d: %w", blockNum, err)
			}
			tx, err = db.BeginRw(ctx)
			if err != nil {
				return err
			}
		}

		// Progress log.
		if time.Since(lastLogTime) > 10*time.Second {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			pct := float64(blockNum) / float64(endBlock) * 100
			blkPerSec := float64(blockNum) / time.Since(t0).Seconds()
			log.Info("Rebuilding PlainState",
				"block", blockNum,
				"pct", fmt.Sprintf("%.1f%%", pct),
				"blk/s", fmt.Sprintf("%.0f", blkPerSec),
				"acctWrites", totalAcct,
				"storWrites", totalStor,
				"allocMB", m.Alloc/1024/1024,
				"elapsed", time.Since(t0).Truncate(time.Second))
			lastLogTime = time.Now()
		}
	}

	// Final commit + progress marker.
	var progBuf [8]byte
	binary.BigEndian.PutUint64(progBuf[:], endBlock-1)
	if err := tx.Put("DbInfo", []byte("ethel_progress"), progBuf[:]); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("final commit: %w", err)
	}

	elapsed := time.Since(t0)
	log.Info("PlainState rebuild complete",
		"blocks", endBlock,
		"acctWrites", totalAcct,
		"storWrites", totalStor,
		"elapsed", elapsed.Truncate(time.Second),
		"blk/s", fmt.Sprintf("%.0f", float64(endBlock)/elapsed.Seconds()))

	// Verify state root.
	verifyStateRoot(ctx, db, inputFreezer, endBlock-1, t0)
	return nil
}

func verifyStateRoot(ctx context.Context, db kv.RwDB, inputFreezer *freezer.Freezer, blockNum uint64, t0 time.Time) {
	log.Info("Computing state root for verification...", "block", blockNum)

	headerData, err := inputFreezer.Ancient(freezer.TableHeaders, blockNum)
	if err != nil {
		log.Warn("Cannot read header", "block", blockNum, "err", err)
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
	if err := tx.Commit(); err != nil {
		return
	}

	tx2, err := db.BeginRw(ctx)
	if err != nil {
		return
	}
	computedRoot, err := CalcStateRoot(tx2)
	tx2.Rollback()
	if err != nil {
		log.Warn("CalcStateRoot failed", "err", err)
		return
	}

	if computedRoot == header.Root {
		log.Info("State root VERIFIED",
			"block", blockNum,
			"root", header.Root.Hex(),
			"elapsed", time.Since(t0).Truncate(time.Second))
	} else {
		log.Error("State root MISMATCH",
			"block", blockNum,
			"computed", computedRoot.Hex(),
			"expected", header.Root.Hex())
	}
}

// Ensure types.Address is used (avoid import cycle).
var _ = types.Address{}
