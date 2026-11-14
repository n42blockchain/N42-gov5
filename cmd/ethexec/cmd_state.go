// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// ethexec state subcommands: sender recovery, rebuild, unwind, scan and clone.

package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/c2h5oh/datasize"
	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func runSenderRecovery(c *cli.Context) error {
	ancientPath := c.String("ancient")
	bodycPath := c.String("bodyc")
	erigonDB := c.String("erigon-db")
	datadir := c.String("datadir")
	startBlock := c.Uint64("start")
	endBlock := c.Uint64("end")

	ctx, cancel := withShutdown()
	defer cancel()

	// Reth MDBX path — read from TransactionSenders.
	if erigonDB != "" {
		logger := log2.New()
		db, err := mdbx.NewMDBX(logger).
			Path(erigonDB).Label(kv.ChainDB).Readonly().Accede().
			DBVerbosity(kv.DBVerbosityLvl(2)).
			Open(context.Background())
		if err != nil {
			return fmt.Errorf("open mdbx: %w", err)
		}
		defer db.Close()
		log.Info("MDBX opened READ-ONLY for senders", "path", erigonDB)

		if endBlock == 0 {
			endBlock = detectRethEndBlock(db)
			if endBlock == 0 {
				return fmt.Errorf("cannot detect end block from MDBX")
			}
			log.Info("Detected end block", "endBlock", endBlock)
		}

		outputDir := filepath.Join(datadir, "chain", "freezer")
		exporter := ethel.NewRethSenderExporter(db, outputDir)
		return exporter.Export(ctx, startBlock, endBlock)
	}

	// Pick header/body source: geth ancient OR N42 columnar bodyc.
	// Both routed through OpenHeadersBodiesSource which auto-detects by
	// probing for headerc.cidx (N42 columnar marker).
	if ancientPath == "" && bodycPath == "" {
		return fmt.Errorf("one of --ancient, --bodyc, or --erigon-db is required")
	}
	if ancientPath != "" && bodycPath != "" {
		return fmt.Errorf("--ancient and --bodyc are mutually exclusive")
	}
	inputPath := ancientPath
	if bodycPath != "" {
		inputPath = bodycPath
	}
	workers := c.Int("workers")
	inputSrc, err := ethel.OpenHeadersBodiesSource(inputPath)
	if err != nil {
		return fmt.Errorf("open input source %s: %w", inputPath, err)
	}
	defer inputSrc.Close()
	log.Info("Sender recovery input source", "path", inputPath, "maxBlock", inputSrc.MaxBlock())

	ancientOut := filepath.Join(datadir, "chain", "freezer")
	if err := os.MkdirAll(ancientOut, 0755); err != nil {
		return err
	}
	of, err := freezer.New(ancientOut, 0)
	if err != nil {
		return fmt.Errorf("open output freezer: %w", err)
	}
	defer of.Close()

	stage := ethel.NewSenderStage(inputSrc, of, params.EthereumMainnetChainConfig, workers)
	if c.IsSet("start") || c.IsSet("end") {
		stage.SetRange(startBlock, endBlock)
	}
	return stage.Run(ctx)
}

func runRebuildState(c *cli.Context) error {
	ancientPath := c.String("ancient")
	datadir := c.String("datadir")
	leavesDir := c.String("leaves")
	startBlock := c.Uint64("start")
	endBlock := c.Uint64("end")
	verifyInterval := c.Uint64("verify")
	evmFromFallback := c.Bool("evm-from-fallback")
	persistTrie := c.Bool("persist-trie")
	startWasSet := c.IsSet("start")
	dirtySpaceGB := c.Uint64("dirty-space-gb")
	if dirtySpaceGB == 0 {
		dirtySpaceGB = 2
	}

	// Optional [TRACK] address. Forwards to modules/state which gates
	// per-address debug prints in PlainStateBuffer / BufferedPlainState
	// reader+writer + ApplyTo.sweep. Validated as 20-byte hex; empty
	// flag = no tracking.
	if rawAddr := c.String("track-addr"); rawAddr != "" {
		s := strings.TrimPrefix(rawAddr, "0x")
		s = strings.TrimPrefix(s, "0X")
		raw, err := hex.DecodeString(s)
		if err != nil || len(raw) != 20 {
			return fmt.Errorf("--track-addr must be 20-byte hex (got len=%d, err=%v)", len(raw), err)
		}
		var a types.Address
		copy(a[:], raw)
		state.SetTrackedAddr(&a)
		log.Info("rebuild-state: TRACK enabled", "addr", a)
	}

	if leavesDir == "" {
		leavesDir = filepath.Join(datadir, "chain", "freezer")
	}

	logger := log2.New()
	// Auto-detect: if MDBX exists at datadir, use Accede (preserve geometry);
	// otherwise create with default geometry (rebuild from scratch).
	hasMDBX := false
	if fi, err := os.Stat(filepath.Join(datadir, "mdbx.dat")); err == nil && fi.Size() > 0 {
		hasMDBX = true
	}
	mdbxBuilder := mdbx.NewMDBX(logger).
		Path(datadir).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(2 * datasize.TB).
		GrowthStep(4 * datasize.GB).
		DirtySpace(dirtySpaceGB * uint64(datasize.GB)).
		DBVerbosity(kv.DBVerbosityLvl(2))
	if hasMDBX {
		mdbxBuilder = mdbxBuilder.Accede()
	}
	db, err := mdbxBuilder.Open(context.Background())
	if err != nil {
		return fmt.Errorf("open mdbx: %w", err)
	}
	defer db.Close()

	// Auto-resume: if --start wasn't provided and the datadir has a
	// recorded ethel_progress, start from that block + 1 instead of 0.
	// Saves the user from having to remember the exact checkpoint height
	// between runs. --start 0 explicitly still clears and rebuilds from
	// genesis.
	//
	// Two progress markers can exist:
	//   DbInfo/ethel_progress              — set ONLY at RebuildState
	//                                        end; STALE if rebuild crashed
	//                                        mid-run. Observed on
	//                                        D:/N42-eth1177 reading 12.5M
	//                                        when the executor had
	//                                        actually pushed PlainState to
	//                                        25.1M via SyncStageProgress.
	//   SyncStageProgress/ethel-last-block — set per-batch by
	//                                        ethel.WriteProgress
	//                                        (progress.go:34) — AUTHORITATIVE.
	// We read BOTH and use max(). If they disagree, log loudly so the
	// operator notices the inconsistency.
	if !startWasSet && hasMDBX {
		roTx, rerr := db.BeginRo(context.Background())
		if rerr == nil {
			var dbInfoProgress, syncProgress uint64
			if v, _ := roTx.GetOne("DbInfo", []byte("ethel_progress")); len(v) == 8 {
				dbInfoProgress = binary.BigEndian.Uint64(v)
			}
			if v, _ := roTx.GetOne(kv.SyncStageProgress, []byte("ethel-last-block")); len(v) == 8 {
				syncProgress = binary.BigEndian.Uint64(v)
			}
			roTx.Rollback()
			progress := dbInfoProgress
			source := "DbInfo/ethel_progress"
			if syncProgress > dbInfoProgress {
				progress = syncProgress
				source = "SyncStageProgress/ethel-last-block"
			}
			if syncProgress != 0 && dbInfoProgress != syncProgress {
				log.Warn("Progress markers disagree — using max (authoritative)",
					"db_info", dbInfoProgress, "sync_stage", syncProgress, "chosen_source", source)
			}
			if progress > 0 {
				startBlock = progress + 1
				log.Info("Auto-resuming",
					"progress", progress, "startBlock", startBlock, "source", source)
			}
		}
	}

	ctx, cancel := withShutdown()
	defer cancel()

	// Open Geth input freezer up front for periodic verify and final verify.
	inputF, err := freezer.NewReadOnly(ancientPath)
	if err != nil {
		return fmt.Errorf("open geth ancient: %w", err)
	}
	defer inputF.Close()

	// Resolve wipes sidecar: explicit flag wins; otherwise auto-detect
	// <leavesDir>/wipes.cidx and use that dir when present.
	wipesSidecar := c.String("wipes-sidecar")
	if wipesSidecar == "" {
		if _, statErr := os.Stat(filepath.Join(leavesDir, freezer.TableWipes+".cidx")); statErr == nil {
			wipesSidecar = leavesDir
			log.Info("Wipes sidecar auto-detected", "dir", wipesSidecar)
		}
	}

	opts := ethel.RebuildOptions{
		StartBlock:      startBlock,
		VerifyInterval:  verifyInterval,
		InputFreezer:    inputF,
		ChainConfig:     params.EthereumMainnetChainConfig,
		GethFreezer:     inputF,
		EVMFromFallback: evmFromFallback,
		PersistTrie:     persistTrie,
		WipesSidecarDir: wipesSidecar,
	}
	if err := ethel.RebuildStateWith(ctx, db, leavesDir, endBlock, opts); err != nil {
		return err
	}
	if c.Bool("skip-final-verify") {
		log.Info("Skipping post-rebuild HPH state-root verify (--skip-final-verify). Trust shifts to upstream bundle hash.")
		return nil
	}
	// Clamp endBlock to the leaves freezer's item count — the rebuild
	// stopped there, so VerifyRebuildRoot must compare OUR state at
	// min(endBlock, freezer.items)-1 against geth's header at that same
	// block. Without this, --end X where X > freezer.items gives a false
	// MISMATCH: our state is at freezer.items-1 but we'd verify at X-1.
	verifyEnd := endBlock
	if t, terr := freezer.NewFreezerTableReadOnly(leavesDir, freezer.TableAccountChanges, "c"); terr == nil {
		if items := t.Items(); verifyEnd == 0 || items < verifyEnd {
			verifyEnd = items
		}
		t.Close()
	}
	ethel.VerifyRebuildRoot(ctx, db, inputF, verifyEnd, min(runtime.NumCPU(), 16))
	return nil
}

func runUnwind(c *cli.Context) error {
	ancientPath := c.String("ancient")
	datadir := c.String("datadir")
	leavesDir := c.String("leaves")
	targetBlock := c.Uint64("target")

	if leavesDir == "" {
		leavesDir = filepath.Join(datadir, "chain", "freezer")
	}

	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(datadir).
		Label(kv.ChainDB).
		Accede().
		DBVerbosity(kv.DBVerbosityLvl(2)).
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open mdbx: %w", err)
	}
	defer db.Close()

	_, cancel := withShutdown()
	defer cancel()

	// Open output freezer (acctcs/storcs) — Reorg reads OLD values from here.
	outFreezer, err := freezer.New(leavesDir, 0)
	if err != nil {
		return fmt.Errorf("open output freezer: %w", err)
	}
	defer outFreezer.Close()

	inputF, err := freezer.NewReadOnly(ancientPath)
	if err != nil {
		return fmt.Errorf("open geth ancient: %w", err)
	}
	defer inputF.Close()

	if err := ethel.Reorg(db, outFreezer, targetBlock); err != nil {
		return fmt.Errorf("reorg: %w", err)
	}
	// Skip verify (too slow for debugging). Use compare-root separately if needed.
	log.Info("Unwind complete (verify skipped)", "target", targetBlock)
	return nil
}

func runScanCS(c *cli.Context) error {
	datadir := c.String("datadir")
	leavesDir := c.String("leaves")
	startBlock := c.Uint64("start")
	endBlock := c.Uint64("end")

	if leavesDir == "" {
		leavesDir = filepath.Join(datadir, "chain", "freezer")
	}

	type tableInfo struct {
		name string
		tbl  *freezer.FreezerTable
		decode func([]byte) error
	}

	acctTbl, err := freezer.NewFreezerTableReadOnly(leavesDir, "acctcs", "c")
	if err != nil {
		return fmt.Errorf("open acctcs: %w", err)
	}
	defer acctTbl.Close()
	acctTbl.ForceBatchSize(freezer.BatchSize)
	acctTbl.SetCompressed(true)

	stoTbl, err := freezer.NewFreezerTableReadOnly(leavesDir, "storcs", "c")
	if err != nil {
		return fmt.Errorf("open storcs: %w", err)
	}
	defer stoTbl.Close()
	stoTbl.ForceBatchSize(freezer.BatchSize)
	stoTbl.SetCompressed(true)

	tables := []tableInfo{
		{"acctcs", acctTbl, func(data []byte) error {
			if len(data) == 0 { return nil }
			_, err := ethel.DecodeAccountChanges(data)
			return err
		}},
		{"storcs", stoTbl, func(data []byte) error {
			if len(data) == 0 { return nil }
			_, err := ethel.DecodeStorageChanges(data)
			return err
		}},
	}

	for _, ti := range tables {
		items := ti.tbl.Items()
		end := endBlock
		if end == 0 || end > items {
			end = items
		}
		start := startBlock
		fmt.Printf("\n=== %s: items=%d scan=%d..%d ===\n", ti.name, items, start, end)

		inBad := false
		badStart := uint64(0)
		badCount := 0
		goodCount := 0
		lastGood := uint64(0)

		// Scan every block individually
		for blk := start; blk < end; blk++ {
			data, err := ti.tbl.Retrieve(blk)
			blockOK := err == nil
			if blockOK {
				blockOK = ti.decode(data) == nil
			}

			if !blockOK {
				if !inBad {
					inBad = true
					badStart = blk
					badCount++
				}
			} else {
				if inBad {
					fmt.Printf("  BAD: blocks %d - %d (%d blocks)\n", badStart, blk-1, blk-badStart)
					inBad = false
				}
				goodCount++
				lastGood = blk
			}
			if blk > 0 && blk%5000000 == 0 {
				fmt.Printf("  ... scanned %d/%d\n", blk, end)
			}
		}
		if inBad {
			fmt.Printf("  BAD: blocks %d - %d (to end)\n", badStart, end-1)
		}
		fmt.Printf("  good batches: %d, bad regions: %d, lastGoodBlock: %d\n", goodCount, badCount, lastGood)
	}
	return nil
}

// runCloneState copies PlainState + progress from src to a freshly
// created dst datadir with 2 TB MapSize. Escapes MapSize cap
// inheritance without rerunning the multi-hour changeset replay.
func runCloneState(c *cli.Context) error {
	srcPath := c.String("src")
	dstPath := c.String("dst")
	batchN := c.Int("batch")
	if batchN <= 0 {
		batchN = 500_000
	}

	// Refuse to write into an existing dst — clone must land fresh so
	// the 2 TB MapSize actually takes effect via SetGeometry.
	if fi, err := os.Stat(filepath.Join(dstPath, "mdbx.dat")); err == nil && fi.Size() > 0 {
		return fmt.Errorf("--dst %s already contains an mdbx.dat (size=%d); delete it first so SetGeometry(2TB) applies to a fresh file",
			dstPath, fi.Size())
	}

	logger := log2.New()

	// Open src read-only, Accede so we inherit whatever geometry it has.
	srcDB, err := mdbx.NewMDBX(logger).
		Path(srcPath).
		Label(kv.ChainDB).
		Accede().
		Readonly().
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open src %s: %w", srcPath, err)
	}
	defer srcDB.Close()

	// Open dst fresh with full 2 TB MapSize + matching page/growth params.
	if err := os.MkdirAll(dstPath, 0755); err != nil {
		return fmt.Errorf("mkdir dst: %w", err)
	}
	dstDB, err := mdbx.NewMDBX(logger).
		Path(dstPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(2 * datasize.TB).
		GrowthStep(4 * datasize.GB).
		DirtySpace(uint64(2 * datasize.GB)).
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open dst %s: %w", dstPath, err)
	}
	defer dstDB.Close()

	ctx, cancel := withShutdown()
	defer cancel()

	log.Info("Cloning PlainState", "src", srcPath, "dst", dstPath, "batch", batchN)
	if err := ethel.CloneState(ctx, srcDB, dstDB, ethel.CloneStateOptions{BatchN: batchN}); err != nil {
		return err
	}
	log.Info("Clone complete. Next step: rebuild-state --datadir "+dstPath+" --persist-trie (auto-resume skips replay)", "dst", dstPath)
	return nil
}
