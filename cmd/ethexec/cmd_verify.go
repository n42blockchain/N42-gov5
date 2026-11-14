// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// ethexec verification subcommands: journal, root, incremental, changeset root and exec compare.

package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/c2h5oh/datasize"
	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

func runVerifyJournal(c *cli.Context) error {
	ancientPath := c.String("ancient")
	datadir := c.String("datadir")
	genesisPath := c.String("genesis")
	verifyInterval := c.Uint64("verify")
	endBlock := c.Uint64("end")

	// Open Geth input freezer (headers).
	inputF, err := freezer.NewReadOnly(ancientPath)
	if err != nil {
		return fmt.Errorf("open input freezer: %w", err)
	}
	defer inputF.Close()

	// Open leaves freezer.
	leavesDir := c.String("leaves")
	if leavesDir == "" {
		leavesDir = filepath.Join(datadir, "chain", "freezer")
	}
	log.Info("Opening leaves freezer", "path", leavesDir)
	outF, err := freezer.New(leavesDir, 0)
	if err != nil {
		return fmt.Errorf("open leaves freezer: %w", err)
	}
	defer outF.Close()

	rebuild := c.Bool("rebuild")
	var dbPath string
	if rebuild {
		dbPath = datadir
		log.Info("REBUILD mode: writing PlainState to datadir MDBX", "path", dbPath)
	} else {
		var err2 error
		dbPath, err2 = os.MkdirTemp("", "ethexec-verify-*")
		if err2 != nil {
			return fmt.Errorf("create temp dir: %w", err2)
		}
		defer os.RemoveAll(dbPath)
		log.Info("Using temp MDBX", "path", dbPath)
	}
	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(dbPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(4 * datasize.GB).
		GrowthStep(1 * datasize.GB).
		DirtySpace(uint64(256 * datasize.MB)).
		DBVerbosity(kv.DBVerbosityLvl(2)).
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open mdbx: %w", err)
	}
	defer db.Close()

	// Load genesis state.
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		return err
	}
	var count int
	if genesisPath != "" {
		log.Info("Loading genesis state", "path", genesisPath)
		count, err = ethel.InitEthGenesisState(tx, genesisPath)
	} else {
		log.Info("Loading genesis state (built-in mainnet)")
		count, err = ethel.InitEthGenesisStateFromBytes(tx, params.EthMainnetGenesisJSON())
	}
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("init genesis: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit genesis: %w", err)
	}
	log.Info("Genesis loaded", "accounts", count)

	verifier := ethel.NewJournalVerifier(db, inputF, outF, verifyInterval, endBlock)

	ctx, cancel := withShutdown()
	defer cancel()
	return verifier.Run(ctx)
}

func runVerifyRoot(c *cli.Context) error {
	ancientPath := c.String("ancient")
	datadir := c.String("datadir")
	blockNum := c.Uint64("block")

	logger := log2.New()
	// MapSize 4 TB so RebuildHashedStateETL can append HashedAccounts +
	// HashedStorage (10M+ accounts × 32M+ storage rows past block 12M)
	// without MDBX_MAP_FULL. NOTE: Accede() short-circuits SetGeometry
	// in lib/kv/mdbx, so MapSize would be ignored if combined; we open
	// as exclusive primary to ensure the geometry is applied. Verify-root
	// is single-shot read+temp-write, fine to hold the DB exclusively.
	db, err := mdbx.NewMDBX(logger).
		Path(datadir).
		Label(kv.ChainDB).
		MapSize(4 * datasize.TB).
		GrowthStep(2 * datasize.GB).
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open mdbx: %w", err)
	}
	defer db.Close()

	inputF, err := freezer.NewReadOnly(ancientPath)
	if err != nil {
		return fmt.Errorf("open geth ancient: %w", err)
	}
	defer inputF.Close()

	workers := c.Int("workers")
	if workers <= 0 {
		workers = min(runtime.NumCPU(), 16)
	}
	// VerifyRebuildRoot takes endBlock (exclusive), verifies at endBlock-1.
	// Pass blockNum+1 so it verifies AT blockNum.
	ethel.VerifyRebuildRoot(context.Background(), db, inputF, blockNum+1, workers)
	return nil
}

func runVerifyIncremental(c *cli.Context) error {
	ancientPath := c.String("ancient")
	datadir := c.String("datadir")
	leavesDir := c.String("leaves")
	startBlock := c.Uint64("start")
	endBlock := c.Uint64("end")
	commitEvery := c.Uint64("commit-every")

	if leavesDir == "" {
		leavesDir = filepath.Join(datadir, "chain", "freezer")
	}

	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(datadir).
		Label(kv.ChainDB).
		Accede().
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open mdbx: %w", err)
	}
	defer db.Close()

	ctx, cancel := withShutdown()
	defer cancel()

	inputF, err := freezer.NewReadOnly(ancientPath)
	if err != nil {
		return fmt.Errorf("open geth ancient: %w", err)
	}
	defer inputF.Close()

	opts := ethel.VerifyIncrementalOptions{
		StartBlock:     startBlock,
		EndBlock:       endBlock,
		LeavesDir:      leavesDir,
		InputFreezer:   inputF,
		CommitEvery:    commitEvery,
		UseIncremental: c.Bool("use-incremental"),
	}
	return ethel.VerifyIncremental(ctx, db, opts)
}

// runVerifyCSRoot: rebuild PlainState from changesets to a given block,
// then compute state root (MPT) and compare against the header.
// No EVM execution. For bisecting changeset corruption.
func runVerifyCSRoot(c *cli.Context) error {
	ancientPath := c.String("ancient")
	datadir := c.String("datadir")
	leavesDir := c.String("leaves")
	targetBlock := c.Uint64("block")

	// 1. Create fresh MDBX.
	if err := os.MkdirAll(datadir, 0755); err != nil {
		return err
	}
	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(datadir).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(64 * datasize.GB).
		GrowthStep(2 * datasize.GB).
		DirtySpace(uint64(512 * datasize.MB)).
		DBVerbosity(kv.DBVerbosityLvl(2)).
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open mdbx: %w", err)
	}
	defer db.Close()

	ctx, cancel := withShutdown()
	defer cancel()

	// 2. Open Geth ancient for header verification.
	inputF, err := freezer.NewReadOnly(ancientPath)
	if err != nil {
		return fmt.Errorf("open geth ancient: %w", err)
	}
	defer inputF.Close()

	// 3. Check current progress — resume from there if already partially built.
	var startBlock uint64
	{
		rtx, err := db.BeginRo(ctx)
		if err != nil {
			return err
		}
		saved := ethel.ReadProgress(rtx)
		rtx.Rollback()
		if saved > 0 && saved < targetBlock {
			startBlock = saved + 1
			log.Info("Resuming rebuild from existing state", "progress", saved, "target", targetBlock)
		} else if saved >= targetBlock {
			log.Info("MDBX already at or past target, skipping rebuild", "progress", saved, "target", targetBlock)
			ethel.VerifyRebuildRoot(ctx, db, inputF, targetBlock+1, min(runtime.NumCPU(), 16))
			return nil
		}
	}

	// Rebuild PlainState from changesets (no EVM, no verify during rebuild).
	endBlock := targetBlock + 1 // RebuildStateWith uses exclusive end
	opts := ethel.RebuildOptions{
		StartBlock:   startBlock,
		InputFreezer: inputF,
		ChainConfig:  params.EthereumMainnetChainConfig,
		GethFreezer:  inputF,
	}
	if err := ethel.RebuildStateWith(ctx, db, leavesDir, endBlock, opts); err != nil {
		return fmt.Errorf("rebuild to block %d: %w", targetBlock, err)
	}

	// Write progress so next run can resume — under BOTH marker keys.
	// rebuild-state's auto-resume reads DbInfo/ethel_progress; forward
	// replay reads SyncStageProgress/ethel-last-block. Skipping either
	// one triggered the "block 0 genesis snapshot exceeds uint16 count"
	// crash on d:\N42-eth1456 / d:\N42-eth1177.
	{
		wtx, err := db.BeginRw(ctx)
		if err != nil {
			return err
		}
		if err := ethel.WriteProgress(wtx, targetBlock); err != nil {
			wtx.Rollback()
			return err
		}
		var pbuf [8]byte
		binary.BigEndian.PutUint64(pbuf[:], targetBlock)
		if err := wtx.Put("DbInfo", []byte("ethel_progress"), pbuf[:]); err != nil {
			wtx.Rollback()
			return err
		}
		if err := wtx.Commit(); err != nil {
			return err
		}
	}

	// 4. Verify state root at targetBlock via MPT.
	ethel.VerifyRebuildRoot(ctx, db, inputF, endBlock, min(runtime.NumCPU(), 16))
	return nil
}

// runExecCompare: EVM execution on primary MDBX + shadow CS rebuild on
// secondary MDBX. At each compare interval, applies changesets to shadow
// and does byte-exact comparison of Account/Storage tables.
// runExecCompare: offline tool — no EVM execution.
// Reads changesets from primary's output freezer, applies to shadow,
// then compares Account/Storage tables. Run AFTER normal ethexec.
//
// Usage:
//   1. ethexec --ancient ... --datadir D:/N42-ethcs1 --verify 1000000  (normal EVM run)
//   2. ethexec exec-compare --primary D:/N42-ethcs1 --shadow D:/N42-ethcs01 --from 6000000 --to 7000000
func runExecCompare(c *cli.Context) error {
	primaryDir := c.String("primary")
	shadowDir := c.String("shadow")
	fromBlock := c.Uint64("from")
	toBlock := c.Uint64("to")

	if primaryDir == "" || shadowDir == "" || toBlock == 0 {
		return fmt.Errorf("--primary, --shadow, --to are required")
	}

	log.Info("exec-compare (offline)", "primary", primaryDir,
		"shadow", shadowDir, "from", fromBlock, "to", toBlock)

	ctx, cancel := withShutdown()
	defer cancel()

	// Step 1: Open shadow writable, apply changesets from primary's freezer.
	freezerPath := filepath.Join(primaryDir, "chain", "freezer")
	log.Info("Applying changesets to shadow", "freezer", freezerPath, "from", fromBlock, "to", toBlock)
	{
		sdb, err := mdbx.NewMDBX(log2.New()).
			Path(shadowDir).Label(kv.ChainDB).
			PageSize(4096).MapSize(2 * datasize.TB).
			GrowthStep(2 * datasize.GB).
			DirtySpace(uint64(256 * datasize.MB)).
			DBVerbosity(kv.DBVerbosityLvl(2)).
			Open(context.Background())
		if err != nil {
			return fmt.Errorf("open shadow: %w", err)
		}
		if err := ethel.ApplyChangesetsToMDBX(ctx, sdb, freezerPath, fromBlock, toBlock); err != nil {
			sdb.Close()
			return fmt.Errorf("apply cs: %w", err)
		}
		sdb.Close()
		log.Info("Changesets applied, shadow closed")
	}

	// Step 2: Open both readonly, compare.
	log.Info("Comparing tables")
	pdb, err := mdbx.NewMDBX(log2.New()).
		Path(primaryDir).Label(kv.ChainDB).
		Readonly().Accede().
		DBVerbosity(kv.DBVerbosityLvl(2)).
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open primary RO: %w", err)
	}
	defer pdb.Close()

	sdb, err := mdbx.NewMDBX(log2.New()).
		Path(shadowDir).Label(kv.ChainDB).
		Readonly().Accede().
		DBVerbosity(kv.DBVerbosityLvl(2)).
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open shadow RO: %w", err)
	}
	defer sdb.Close()

	ptx, err := pdb.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer ptx.Rollback()

	stx, err := sdb.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer stx.Rollback()

	results, err := ethel.CompareMDBXTables(ptx, stx, 20)
	if err != nil {
		return fmt.Errorf("compare: %w", err)
	}
	for _, r := range results {
		if !r.OK {
			return fmt.Errorf("MISMATCH: %s first=%s mismatches=%d onlyPrimary=%d onlyShadow=%d",
				r.Table, r.FirstDiff, r.Mismatches, r.OnlyInA, r.OnlyInB)
		}
	}
	log.Info("ALL TABLES MATCH", "block", toBlock-1)
	return nil
}
