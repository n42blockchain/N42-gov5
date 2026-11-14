// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// ethexec compaction subcommands: changesets, storage changesets, bodies, headers, receipts.

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func runCSAnalyze(c *cli.Context) error {
	erigonDB := c.String("erigon-db")
	sampleBlocks := c.Uint64("sample")

	// Open Erigon MDBX READ-ONLY (Accede = open existing, no create/modify).
	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(erigonDB).
		Label(kv.ChainDB).
		Readonly(). // CRITICAL: read-only access to protect source data
		Accede().   // accept existing DB parameters
		DBVerbosity(kv.DBVerbosityLvl(2)).
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open erigon mdbx: %w", err)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		return fmt.Errorf("begin ro tx: %w", err)
	}
	defer tx.Rollback()

	result, err := cscompact.AnalyzeChangesets(tx, sampleBlocks)
	if err != nil {
		return err
	}

	log.Info("=== Analysis Summary ===")
	log.Info("AccountCS",
		"entries", result.AccTotalEntries,
		"uniqueAddrs", result.AccUniqueAddrs,
		"avgVal", fmt.Sprintf("%.1fB", result.AccAvgValLen),
		"zeroVals", result.AccZeroValues)
	for i, a := range result.TopAccAddrs {
		log.Info(fmt.Sprintf("  Top Account #%d", i+1),
			"addr", fmt.Sprintf("%x", a.Addr[:6]),
			"count", a.Count)
	}
	log.Info("StorageCS",
		"entries", result.StoTotalEntries,
		"uniqueAddrs", result.StoUniqueAddrs,
		"avgVal", fmt.Sprintf("%.1fB", result.StoAvgValLen),
		"zeroVals", result.StoZeroValues)
	for i, a := range result.TopStoAddrs {
		log.Info(fmt.Sprintf("  Top Storage #%d", i+1),
			"addr", fmt.Sprintf("%x", a.Addr[:6]),
			"count", a.Count)
	}
	return nil
}

func runCSCompact(c *cli.Context) error {
	erigonDB := c.String("erigon-db")
	datadir := c.String("datadir")
	startBlock := c.Uint64("start")
	endBlock := c.Uint64("end")

	// Open Erigon MDBX READ-ONLY.
	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(erigonDB).
		Label(kv.ChainDB).
		Readonly().
		Accede().
		DBVerbosity(kv.DBVerbosityLvl(2)).
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open erigon mdbx: %w", err)
	}
	defer db.Close()
	log.Info("Erigon MDBX opened READ-ONLY", "path", erigonDB)

	// Determine end block from changeset tables (NOT history — history
	// uses 0xFFFFFFFF shard markers that overflow).
	if endBlock == 0 {
		tx, err := db.BeginRo(context.Background())
		if err != nil {
			return err
		}
		// Try Reth names first (with 's'), then Erigon (no 's').
		for _, tbl := range []string{
			"AccountChangeSets", "StorageChangeSets",
			"AccountChangeSet", "StorageChangeSet",
		} {
			cursor, err := tx.Cursor(tbl)
			if err != nil {
				continue
			}
			k, v, err := cursor.Last()
			cursor.Close()
			if err != nil || k == nil || len(k) < 8 {
				continue
			}
			// Skip root DBI fallback (large blob values, not real changesets).
			if len(v) > 64 {
				continue
			}
			bn := cscompact.DetectBlockNum(k)
			log.Info("Table last key", "table", tbl, "keyLen", len(k), "blockNum", bn)
			if bn > 0 && bn < 1<<32 && bn+1 > endBlock {
				endBlock = bn + 1
			}
		}
		tx.Rollback()
		if endBlock == 0 {
			return fmt.Errorf("cannot determine end block from changeset tables")
		}
		log.Info("Detected end block", "endBlock", endBlock)
	}

	outputDir := filepath.Join(datadir, "chain", "freezer")

	ctx, cancel := withShutdown()
	defer cancel()

	// Detect table names (Erigon vs Reth).
	acctTable := detectRealTable(db, "AccountChangeSets", "AccountChangeSet")
	storTable := detectRealTable(db, "StorageChangeSets", "StorageChangeSet")

	log.Info("=== AccountCS compression ===", "table", acctTable)
	accComp := cscompact.NewAccountCSCompactor(db, outputDir)
	accComp.SetTableName(acctTable)
	if err := accComp.Run(ctx, startBlock, endBlock); err != nil {
		return fmt.Errorf("account cs: %w", err)
	}

	log.Info("=== StorageCS compression ===", "table", storTable)
	stoComp := cscompact.NewStorageCSCompactor(db, outputDir)
	stoComp.SetTableName(storTable)
	return stoComp.Run(ctx, startBlock, endBlock)
}

func runStorCSCompact(c *cli.Context) error {
	db, err := openErigonDB(c.String("erigon-db"))
	if err != nil {
		return err
	}
	defer db.Close()

	startBlock := c.Uint64("start")
	endBlock := c.Uint64("end")
	if endBlock == 0 {
		endBlock, err = detectEndBlock(db, "StorageChangeSets", "StorageChangeSet")
		if err != nil {
			return err
		}
	}

	ctx, cancel := withShutdown()
	defer cancel()

	storTable := detectRealTable(db, "StorageChangeSets", "StorageChangeSet")
	log.Info("=== StorageCS compression ===", "table", storTable)
	stoComp := cscompact.NewStorageCSCompactor(db, filepath.Join(c.String("datadir"), "chain"))
	stoComp.SetTableName(storTable)
	return stoComp.Run(ctx, startBlock, endBlock)
}

func runBodyCompact(c *cli.Context) error {
	ancientPath := c.String("ancient")
	datadir := c.String("datadir")

	f, err := freezer.NewReadOnly(ancientPath)
	if err != nil {
		return fmt.Errorf("open input freezer: %w", err)
	}
	defer f.Close()
	log.Info("Input freezer opened", "frozen", f.Frozen())

	outputDir := filepath.Join(datadir, "chain", "freezer")
	stage := ethel.NewBodyCompactStage(f, outputDir)
	// Log what layout this run actually produced: a regenerated set and a
	// legacy set are indistinguishable by filename.
	if fb := c.Int("frame-blocks"); fb >= 0 {
		stage.SetFrameSize(fb)
	}
	log.Info("Segment framing", "frameBlocks", stage.FrameSize())

	ctx, cancel := withShutdown()
	defer cancel()
	return stage.Run(ctx)
}

func runHeaderCompact(c *cli.Context) error {
	ancientPath := c.String("ancient")
	datadir := c.String("datadir")

	f, err := freezer.NewReadOnly(ancientPath)
	if err != nil {
		return fmt.Errorf("open input freezer: %w", err)
	}
	defer f.Close()
	log.Info("Input freezer opened", "frozen", f.Frozen())

	outputDir := filepath.Join(datadir, "chain", "freezer")

	stage := ethel.NewHeaderCompactStage(f, outputDir)
	// Log what layout this run actually produced: a regenerated set and a
	// legacy set are indistinguishable by filename.
	if fb := c.Int("frame-blocks"); fb >= 0 {
		stage.SetFrameSize(fb)
	}
	log.Info("Segment framing", "frameBlocks", stage.FrameSize())

	ctx, cancel := withShutdown()
	defer cancel()
	return stage.Run(ctx)
}

func runCompact(c *cli.Context) error {
	srcDir := filepath.Join(c.String("datadir"), "ancient")
	dstDir := filepath.Join(c.String("output"), "ancient")

	log.Info("Compacting freezer tables", "src", srcDir, "dst", dstDir)
	if err := freezer.CompactAll(srcDir, dstDir); err != nil {
		return err
	}
	log.Info("Compaction complete")
	return nil
}

func runReceiptCopy(c *cli.Context) error {
	ancientPath := c.String("ancient")
	datadir := c.String("datadir")
	workers := c.Int("workers")

	// Open Geth input freezer (read-only).
	f, err := freezer.NewReadOnly(ancientPath)
	if err != nil {
		return fmt.Errorf("open input freezer: %w", err)
	}
	defer f.Close()
	log.Info("Input freezer opened", "frozen", f.Frozen())

	// Write receipts to the output ancient directory.
	ancientOut := filepath.Join(datadir, "chain", "freezer")
	if err := os.MkdirAll(ancientOut, 0755); err != nil {
		return err
	}
	of, err := freezer.New(ancientOut, 0)
	if err != nil {
		return fmt.Errorf("open output freezer: %w", err)
	}
	defer of.Close()

	stage := ethel.NewReceiptStage(f, of, workers)

	ctx, cancel := withShutdown()
	defer cancel()
	return stage.Run(ctx)
}

// cloneTables copies Account, Storage and Code tables from src to dst
// with batched commits to stay within MDBX dirty-space limits.
func cloneTables(src kv.RwDB, dst kv.RwDB, progressBlock uint64) error {
	srcTx, err := src.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer srcTx.Rollback()

	const batchSize = 2_000_000

	for _, table := range []string{"Account", "Storage", "Code"} {
		log.Info("Cloning table (this may take a while for Storage)...", "table", table)
		t0 := time.Now()

		dstTx, err := dst.BeginRw(context.Background())
		if err != nil {
			return err
		}
		cursor, err := srcTx.Cursor(table)
		if err != nil {
			dstTx.Rollback()
			return err
		}

		var count uint64
		var lastKey []byte
		for k, v, cerr := cursor.First(); k != nil && cerr == nil; k, v, cerr = cursor.Next() {
			if err := dstTx.Put(table, k, v); err != nil {
				cursor.Close()
				dstTx.Rollback()
				return fmt.Errorf("put %s: %w", table, err)
			}
			lastKey = append(lastKey[:0], k...)
			count++
			if count%2_000_000 == 0 {
				log.Info("  cloning", "table", table, "entries", count,
					"elapsed", time.Since(t0).Truncate(time.Second))
			}
			if count%batchSize == 0 {
				cursor.Close()
				if err := dstTx.Commit(); err != nil {
					return fmt.Errorf("commit %s at %d: %w", table, count, err)
				}
				dstTx, err = dst.BeginRw(context.Background())
				if err != nil {
					return err
				}
				cursor, err = srcTx.Cursor(table)
				if err != nil {
					dstTx.Rollback()
					return err
				}
				k, v, cerr = cursor.Seek(lastKey)
				if k != nil && cerr == nil && bytes.Equal(k, lastKey) {
					k, v, cerr = cursor.Next()
				}
				if k == nil {
					break
				}
				// Re-enter the loop body for this new k,v.
				if err := dstTx.Put(table, k, v); err != nil {
					cursor.Close()
					dstTx.Rollback()
					return err
				}
				lastKey = append(lastKey[:0], k...)
				count++
			}
		}
		cursor.Close()

		if err := dstTx.Commit(); err != nil {
			return fmt.Errorf("final commit %s: %w", table, err)
		}
		log.Info("  table cloned", "table", table, "entries", count,
			"elapsed", time.Since(t0).Truncate(time.Second))
	}

	// Write progress marker.
	wt, err := dst.BeginRw(context.Background())
	if err != nil {
		return err
	}
	ethel.WriteProgress(wt, progressBlock)
	return wt.Commit()
}
