// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// ethexec re-executes Ethereum mainnet blocks from Geth ancient data
// to produce PlainState, changesets, and receipts.
//
// Usage:
//
//	ethexec --ancient <geth-ancient-dir> --datadir <output-dir> [--genesis <genesis.json>] [--start N] [--end N]

package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/txlookup"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
	"github.com/urfave/cli/v2"
)

// withShutdown creates a context that cancels on SIGINT/SIGTERM.
// A second signal forces immediate exit.
func withShutdown() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Info("Received shutdown signal...")
		cancel()
		<-sig
		os.Exit(1)
	}()
	return ctx, cancel
}

func main() {
	execFlags := []cli.Flag{
		&cli.StringFlag{Name: "ancient", Usage: "Path to Geth ancient chain directory"},
		&cli.StringFlag{Name: "datadir", Usage: "Path to output MDBX database"},
		&cli.StringFlag{Name: "genesis", Usage: "Path to Ethereum genesis.json (default: built-in mainnet)"},
		&cli.Uint64Flag{Name: "start", Usage: "Start block number", Value: 0},
		&cli.Uint64Flag{Name: "end", Usage: "End block number (0 = all available)", Value: 0},
		&cli.Uint64Flag{Name: "commit", Usage: "Commit interval (blocks)", Value: 10000},
		&cli.Uint64Flag{Name: "verify", Usage: "State root verification interval (0=disabled)", Value: 0},
		&cli.BoolFlag{Name: "skip-errors", Usage: "Log gas mismatches but continue execution"},
		&cli.BoolFlag{Name: "no-outputs", Usage: "Skip writing output freezer (receipts, senders, witness, etc.)"},
		&cli.BoolFlag{Name: "pprof", Usage: "Enable mutex/block profiling for pprof flame graphs"},
	}

	// pprof server for flame graphs (always on, profiling hooks only with --pprof).
	go func() { http.ListenAndServe("localhost:6060", nil) }()

	app := &cli.App{
		Name:   "ethexec",
		Usage:  "Re-execute Ethereum mainnet blocks from Geth ancient data",
		Flags:  execFlags,
		Action: run,
		Commands: []*cli.Command{
			{
				Name:  "dumpgenesis",
				Usage: "Export the built-in Ethereum mainnet genesis JSON to stdout or file",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "output", Aliases: []string{"o"}, Usage: "Output file path (default: stdout)"},
				},
				Action: func(c *cli.Context) error {
					data := params.EthMainnetGenesisJSON()
					if out := c.String("output"); out != "" {
						if err := os.WriteFile(out, data, 0644); err != nil {
							return err
						}
						fmt.Printf("Genesis written to %s (%d bytes)\n", out, len(data))
						return nil
					}
					os.Stdout.Write(data)
					return nil
				},
			},
			{
				Name:  "compact",
				Usage: "Batch-compress all output freezer tables to a new directory",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "datadir", Usage: "Source data directory", Required: true},
					&cli.StringFlag{Name: "output", Usage: "Destination directory for compacted files", Required: true},
				},
				Action: runCompact,
			},
			{
				Name:  "sender-recovery",
				Usage: "Extract senders from Geth ancient bodies or Reth MDBX",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "ancient", Usage: "Path to Geth ancient chain directory"},
					&cli.StringFlag{Name: "erigon-db", Usage: "Path to Reth/Erigon MDBX (reads TransactionSenders)"},
					&cli.StringFlag{Name: "datadir", Usage: "Path to output directory", Required: true},
					&cli.IntFlag{Name: "workers", Usage: "Number of parallel workers (Geth mode only)", Value: 0},
					&cli.Uint64Flag{Name: "start", Usage: "Start block", Value: 0},
					&cli.Uint64Flag{Name: "end", Usage: "End block (0=all)", Value: 0},
				},
				Action: runSenderRecovery,
			},
			{
				Name:  "receipt-copy",
				Usage: "Parallel receipt copy from Geth ancient freezer (decode + compact + batch-64)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "ancient", Usage: "Path to Geth ancient chain directory", Required: true},
					&cli.StringFlag{Name: "datadir", Usage: "Path to output directory", Required: true},
					&cli.IntFlag{Name: "workers", Usage: "Number of parallel workers", Value: 0},
				},
				Action: runReceiptCopy,
			},
			{
				Name:  "header-compact",
				Usage: "Columnar-compress headers from Geth ancient (8192-block segments, delta+dict+zstd)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "ancient", Usage: "Path to Geth ancient chain directory", Required: true},
					&cli.StringFlag{Name: "datadir", Usage: "Path to output directory", Required: true},
				},
				Action: runHeaderCompact,
			},
			{
				Name:  "body-compact",
				Usage: "Columnar-compress bodies from Geth ancient (8192-block segments, freezer-style multi-file)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "ancient", Usage: "Path to Geth ancient chain directory", Required: true},
					&cli.StringFlag{Name: "datadir", Usage: "Path to output directory", Required: true},
				},
				Action: runBodyCompact,
			},
			{
				Name:  "verify-journal",
				Usage: "Replay leaves_journal to rebuild PlainState, verify state root against headers",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "ancient", Usage: "Path to Geth ancient chain directory", Required: true},
					&cli.StringFlag{Name: "datadir", Usage: "Path to temp MDBX (or rebuild target)", Required: true},
					&cli.StringFlag{Name: "leaves", Usage: "Path to leaves freezer dir (default: datadir/chain/freezer)"},
					&cli.StringFlag{Name: "genesis", Usage: "Path to Ethereum genesis.json", Required: true},
					&cli.Uint64Flag{Name: "verify", Usage: "Verify state root every N blocks (0=disabled)", Value: 100000},
					&cli.Uint64Flag{Name: "end", Usage: "End block (0=all)", Value: 0},
					&cli.BoolFlag{Name: "rebuild", Usage: "Write PlainState to datadir MDBX (not temp)"},
				},
				Action: runVerifyJournal,
			},
			{
				Name:  "rebuild-state",
				Usage: "Rebuild PlainState from leaves journal (genesis self-contained in block 0)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "ancient", Usage: "Path to Geth ancient chain directory (for header verification)", Required: true},
					&cli.StringFlag{Name: "datadir", Usage: "Path to output MDBX directory", Required: true},
					&cli.StringFlag{Name: "leaves", Usage: "Path to leaves freezer dir (default: <datadir>/chain/freezer)"},
					&cli.Uint64Flag{Name: "start", Usage: "Start block (>0 = resume mode, do NOT clear existing PlainState)", Value: 0},
					&cli.Uint64Flag{Name: "end", Usage: "End block (0=all available)", Value: 0},
					&cli.Uint64Flag{Name: "verify", Usage: "Periodic state-root verify interval (0=verify only at end)", Value: 0},
				},
				Action: runRebuildState,
			},
			{
				Name:  "unwind",
				Usage: "Roll PlainState back to --target by applying OLD values from V2 changesets, then verify root",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "ancient", Usage: "Path to Geth ancient chain directory (for header verification)", Required: true},
					&cli.StringFlag{Name: "datadir", Usage: "Path to existing MDBX directory whose PlainState will be rolled back", Required: true},
					&cli.StringFlag{Name: "leaves", Usage: "Path to V2 changesets freezer dir (default: <datadir>/chain/freezer)"},
					&cli.Uint64Flag{Name: "target", Usage: "Target block (PlainState ends up at this height)", Required: true},
				},
				Action: runUnwind,
			},
			{
				Name:  "reset-progress",
				Usage: "Force-set the ethel progress marker without touching PlainState",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "datadir", Usage: "Path to MDBX directory", Required: true},
					&cli.Uint64Flag{Name: "target", Usage: "Block number to set as last-committed", Required: true},
				},
				Action: func(c *cli.Context) error {
					logger := log2.New()
					db, err := mdbx.NewMDBX(logger).
						Path(c.String("datadir")).
						Label(kv.ChainDB).
						Accede().
						DBVerbosity(kv.DBVerbosityLvl(2)).
						Open(context.Background())
					if err != nil {
						return fmt.Errorf("open mdbx: %w", err)
					}
					defer db.Close()
					tx, err := db.BeginRw(context.Background())
					if err != nil {
						return err
					}
					defer tx.Rollback()
					old := ethel.ReadProgress(tx)
					target := c.Uint64("target")
					if err := ethel.WriteProgress(tx, target); err != nil {
						return err
					}
					if err := tx.Commit(); err != nil {
						return err
					}
					log.Info("Progress reset", "old", old, "new", target)
					return nil
				},
			},
			{
				Name:  "scan-cs",
				Usage: "Scan changeset tables for corruption, report bad batch ranges",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "datadir", Usage: "Path to data directory with output freezer", Required: true},
					&cli.StringFlag{Name: "leaves", Usage: "Path to freezer dir (default: <datadir>/chain/freezer)"},
					&cli.Uint64Flag{Name: "start", Usage: "Start block", Value: 0},
					&cli.Uint64Flag{Name: "end", Usage: "End block (0=all)", Value: 0},
				},
				Action: runScanCS,
			},
			{
				Name:  "compare-root",
				Usage: "Compute state root at a given block via Traditional MPT and HPH, compare with header",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "datadir", Usage: "Path to MDBX with populated PlainState", Required: true},
					&cli.StringFlag{Name: "ancient", Usage: "Path to Geth ancient chain directory (for header root)", Required: true},
					&cli.Uint64Flag{Name: "block", Usage: "Block number to verify", Required: true},
					&cli.StringFlag{Name: "mode", Usage: "mpt | hph | both", Value: "both"},
				},
				Action: runCompareRoot,
			},
			{
				Name:  "verify-senders",
				Usage: "Spot-check pre-computed senders against ecrecover (random sampling)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "ancient", Usage: "Path to Geth ancient chain directory", Required: true},
					&cli.StringFlag{Name: "datadir", Usage: "Path to chain directory (with senders.cidx)", Required: true},
					&cli.IntFlag{Name: "samples", Usage: "Number of random blocks to check", Value: 200},
				},
				Action: func(c *cli.Context) error {
					ancientPath := c.String("ancient")
					chainDir := c.String("datadir") + "/chain"
					samples := c.Int("samples")
					chainCfg := params.MainnetChainConfig
					return verifySenders(chainDir, ancientPath, chainCfg, samples)
				},
			},
			{
				Name:  "mem-test",
				Usage: "Open MDBX, optionally clear tables, print memory (debug)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "datadir", Required: true},
					&cli.BoolFlag{Name: "clear", Usage: "Test ClearBucket on Account+Storage"},
				},
				Action: func(c *cli.Context) error {
					datadir := c.String("datadir")
					doClear := c.Bool("clear")
					logger := log2.New()
					db, err := mdbx.NewMDBX(logger).
						Path(datadir).Label(kv.ChainDB).
						PageSize(4096).
						MapSize(64 * datasize.GB).
						GrowthStep(1 * datasize.GB).
						DirtySpace(uint64(128 * datasize.MB)).
						DBVerbosity(kv.DBVerbosityLvl(2)).
						Open(context.Background())
					if err != nil {
						return err
					}
					defer db.Close()

					// Count entries.
					tx, _ := db.BeginRo(context.Background())
					for _, tbl := range []string{"Account", "Storage", "PlainContractCode", "HashedAccounts", "HashedStorage"} {
						cursor, err := tx.Cursor(tbl)
						if err != nil { continue }
						k, _, _ := cursor.First()
						if k == nil {
							log.Info("Table empty", "table", tbl)
						} else {
							count, _ := cursor.Count()
							log.Info("Table", "name", tbl, "entries", count)
						}
						cursor.Close()
					}
					tx.Rollback()

					var m runtime.MemStats
					runtime.ReadMemStats(&m)
					log.Info("After open", "goAllocMB", m.Alloc/1e6)
					log.Info("Check Task Manager Commit Size NOW. Press Enter to continue...")
					fmt.Scanln()

					if doClear {
						for _, tbl := range []string{"Account", "Storage"} {
							log.Info("ClearBucket starting", "table", tbl)
							t0 := time.Now()
							tx2, _ := db.BeginRw(context.Background())
							if err := tx2.ClearBucket(tbl); err != nil {
								tx2.Rollback()
								log.Error("ClearBucket failed", "table", tbl, "err", err)
								continue
							}
							if err := tx2.Commit(); err != nil {
								log.Error("Commit failed", "table", tbl, "err", err)
								continue
							}
							log.Info("ClearBucket done", "table", tbl, "elapsed", time.Since(t0))
							runtime.ReadMemStats(&m)
							log.Info("After clear", "table", tbl, "goAllocMB", m.Alloc/1e6)
							log.Info("Check Task Manager Commit Size NOW. Press Enter to continue...")
							fmt.Scanln()
						}
					}

					log.Info("Done. Press Ctrl+C to exit")
					select {}
				},
			},
			{
				Name:  "txlookup-build",
				Usage: "Build RecSplit segments for tx hash → block number lookup",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "ancient", Usage: "Path to Geth ancient chain directory"},
					&cli.StringFlag{Name: "erigon-db", Usage: "Path to Reth/Erigon MDBX (reads TransactionHashNumbers)"},
					&cli.StringFlag{Name: "datadir", Usage: "Path to output directory", Required: true},
					&cli.Uint64Flag{Name: "start", Usage: "Start block", Value: 0},
					&cli.Uint64Flag{Name: "end", Usage: "End block (0=all)", Value: 0},
				},
				Action: runTxLookupBuild,
			},
			{
				Name:  "cs-analyze",
				Usage: "Analyze Erigon changeset tables for compression (READ-ONLY)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "erigon-db", Usage: "Path to Erigon chaindata directory", Required: true},
					&cli.Uint64Flag{Name: "sample", Usage: "Sample N blocks (0=all)", Value: 100000},
				},
				Action: runCSAnalyze,
			},
			{
				Name:  "cs-compact",
				Usage: "Compress Erigon changeset tables into columnar segments (READ-ONLY source)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "erigon-db", Usage: "Path to Erigon chaindata directory", Required: true},
					&cli.StringFlag{Name: "datadir", Usage: "Path to output directory", Required: true},
					&cli.Uint64Flag{Name: "start", Usage: "Start block", Value: 0},
					&cli.Uint64Flag{Name: "end", Usage: "End block (0=all in DB)", Value: 0},
				},
				Action: runCSCompact,
			},
			{
				Name:  "storcs-compact",
				Usage: "Compress only StorageChangeSets (skip AccountCS)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "erigon-db", Usage: "Path to Erigon/Reth MDBX", Required: true},
					&cli.StringFlag{Name: "datadir", Usage: "Path to output directory", Required: true},
					&cli.Uint64Flag{Name: "start", Usage: "Start block", Value: 0},
					&cli.Uint64Flag{Name: "end", Usage: "End block (0=all)", Value: 0},
				},
				Action: runStorCSCompact,
			},
			{
				Name:  "storhist-build",
				Usage: "Build only StorageHistory RecSplit segments",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "erigon-db", Usage: "Path to Erigon/Reth MDBX", Required: true},
					&cli.StringFlag{Name: "datadir", Usage: "Path to output directory", Required: true},
					&cli.Uint64Flag{Name: "start", Usage: "Start block", Value: 0},
					&cli.Uint64Flag{Name: "end", Usage: "End block (0=auto)", Value: 0},
				},
				Action: runStorHistBuild,
			},
			{
				Name:  "history-build",
				Usage: "Build RecSplit history segments from Erigon MDBX (READ-ONLY)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "erigon-db", Usage: "Path to Erigon chaindata directory", Required: true},
					&cli.StringFlag{Name: "datadir", Usage: "Path to output directory", Required: true},
					&cli.Uint64Flag{Name: "start", Usage: "Start block", Value: 0},
					&cli.Uint64Flag{Name: "end", Usage: "End block (0=auto)", Value: 0},
					&cli.BoolFlag{Name: "from-changesets", Usage: "Build from changeset tables (fast) instead of history bitmaps (slow)"},
				},
				Action: runHistoryBuild,
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(c *cli.Context) error {
	ancientPath := c.String("ancient")
	datadir := c.String("datadir")
	if ancientPath == "" || datadir == "" {
		return fmt.Errorf("--ancient and --datadir are required")
	}
	if c.Bool("pprof") {
		runtime.SetMutexProfileFraction(5)
		runtime.SetBlockProfileRate(5)
	}
	genesisPath := c.String("genesis")
	startBlock := c.Uint64("start")
	endBlock := c.Uint64("end")
	commitInterval := c.Uint64("commit")
	verifyInterval := c.Uint64("verify")
	skipErrors := c.Bool("skip-errors")
	noOutputs := c.Bool("no-outputs")

	// Open Geth ancient freezer.
	log.Info("Opening Geth ancient data", "path", ancientPath)
	f, err := freezer.New(ancientPath, 0)
	if err != nil {
		return fmt.Errorf("open freezer: %w", err)
	}
	defer f.Close()
	log.Info("Freezer opened", "frozen", f.Frozen())

	// Open MDBX with optimized parameters for ETH EL execution.
	// Full ETH state: ~185GB data + 30% B+tree overhead ≈ 240GB.
	// PageSize 4KB matches OS page size for optimal mmap performance.
	// WriteMap: direct mmap writes (no shadow copy), ~20% faster on Windows.
	// DirtySpace 1GB: allows large write batches before spill (128GB RAM machine).
	// MapSize: Windows counts mmap reservation as committed memory.
	// Keep conservative to avoid OOM. MDBX auto-grows via GrowthStep.
	log.Info("Opening MDBX database", "path", datadir)
	if err := os.MkdirAll(datadir, 0755); err != nil {
		return err
	}
	logger := log2.New()
	// Windows counts writable mmap as committed memory. Avoid WriteMap()
	// and use conservative MapSize. MDBX auto-grows via GrowthStep.
	// Reth's 2.1TB MDBX works fine because it opens Readonly (no commit charge).
	db, err := mdbx.NewMDBX(logger).
		Path(datadir).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(64 * datasize.GB).
		GrowthStep(2 * datasize.GB).
		WriteMergeThreshold(4 * 8192).
		DirtySpace(uint64(512 * datasize.MB)).
		DBVerbosity(kv.DBVerbosityLvl(2)).
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open mdbx: %w", err)
	}
	defer db.Close()

	// Load genesis state.
	{
		tx, err := db.BeginRw(context.Background())
		if err != nil {
			return err
		}
		var count int
		if genesisPath != "" {
			log.Info("Loading Ethereum genesis state", "path", genesisPath)
			count, err = ethel.InitEthGenesisState(tx, genesisPath)
		} else {
			log.Info("Loading Ethereum genesis state (built-in mainnet)")
			count, err = ethel.InitEthGenesisStateFromBytes(tx, params.EthMainnetGenesisJSON())
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("init genesis: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit genesis: %w", err)
		}
		log.Info("Genesis state loaded", "accounts", count)
	}

	// Open output freezer in chain/ directory.
	// Uses empty coreTableSpecs — no auto-create/truncate of headers/bodies.
	// All tables are opened on-demand via EnsureTableCompressed.
	// Output freezer in chain/freezer/ — isolated from compact files in chain/.
	outFreezerPath := filepath.Join(datadir, "chain", "freezer")
	if err := os.MkdirAll(outFreezerPath, 0755); err != nil {
		return err
	}
	outFreezer, err := freezer.New(outFreezerPath, 0)
	if err != nil {
		return fmt.Errorf("open output freezer: %w", err)
	}
	defer outFreezer.Close()
	log.Info("Output freezer opened", "path", outFreezerPath)

	// Set up executor.
	chainCfg := params.EthereumMainnetChainConfig
	engine := ethel.NewEthReplayEngine(chainCfg)

	cfg := ethel.ExecutorConfig{
		StartBlock:     startBlock,
		EndBlock:       endBlock,
		CommitInterval: commitInterval,
		VerifyInterval: verifyInterval,
		SkipErrors:     skipErrors,
		NoOutputs:      noOutputs,
	}

	executor := ethel.NewExecutor(f, db, chainCfg, engine, cfg, outFreezer)

	// Pre-computed senders (input data). Auto-detect:
	// 1. Freezer table format (senders.cidx in output freezer) — from sender-recovery --ancient
	// 2. SegmentStore format (chain/senders.cidx) — from sender-recovery --erigon-db
	senderFound := false
	// Check if the output freezer already has a senders table (same Freezer instance, no re-open).
	if tbl := outFreezer.Table("senders"); tbl != nil && tbl.Items() > 0 {
		executor.SetSenderFreezer(outFreezer)
		log.Info("Pre-computed senders loaded (freezer)", "path", outFreezerPath, "items", tbl.Items())
		senderFound = true
	}
	// Fallback: SegmentStore format.
	if !senderFound {
		for _, dir := range []string{filepath.Join(datadir, "chain"), ancientPath} {
			sr, sErr := ethel.OpenSenderStore(dir)
			if sErr != nil || sr == nil {
				continue
			}
			executor.SetSenderStore(sr)
			log.Info("Pre-computed senders loaded (segment)", "path", dir, "maxBlock", sr.MaxBlock())
			senderFound = true
			break
		}
	}
	if !senderFound {
		log.Info("No pre-computed senders, using ecrecover from signatures")
	}

	// TODO: compact body reader has signature decoding issues (V/R/S columns).
	// Disabled until fixed. Using Geth freezer for now.
	if false {
	for _, dir := range []string{filepath.Join(datadir, "chain"), filepath.Join(datadir, "ancient")} {
		idxPath := filepath.Join(dir, "headers.cidx")
		if _, err := os.Stat(idxPath); err != nil {
			continue
		}
		hr, err := ethel.OpenHeaderCompact(dir)
		if err != nil {
			log.Warn("Cannot open compact headers", "dir", dir, "err", err)
			continue
		}
		br, err := ethel.OpenBodyCompact(dir)
		if err != nil {
			hr.Close()
			log.Warn("Cannot open compact bodies", "dir", dir, "err", err)
			continue
		}
		executor.SetCompactReaders(hr, br)
		log.Info("Using compact headers/bodies", "dir", dir)
		break
	}
	} // end if false

	ctx, cancel := withShutdown()
	defer cancel()
	return executor.Run(ctx)
}

func runVerifyJournal(c *cli.Context) error {
	ancientPath := c.String("ancient")
	datadir := c.String("datadir")
	genesisPath := c.String("genesis")
	verifyInterval := c.Uint64("verify")
	endBlock := c.Uint64("end")

	// Open Geth input freezer (headers).
	inputF, err := freezer.New(ancientPath, 0)
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

	outputDir := filepath.Join(datadir, "chain")

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

// openErigonDB opens an Erigon/Reth MDBX database in read-only Accede mode.
func openErigonDB(path string) (kv.RwDB, error) {
	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(path).Label(kv.ChainDB).Readonly().Accede().
		DBVerbosity(kv.DBVerbosityLvl(2)).
		Open(context.Background())
	if err != nil {
		return nil, fmt.Errorf("open erigon mdbx: %w", err)
	}
	log.Info("Erigon MDBX opened READ-ONLY", "path", path)
	return db, nil
}

// detectEndBlock probes the given table names and returns the highest block+1.
func detectEndBlock(db kv.RwDB, tables ...string) (uint64, error) {
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var endBlock uint64
	for _, tbl := range tables {
		cursor, err := tx.Cursor(tbl)
		if err != nil {
			continue
		}
		k, v, _ := cursor.Last()
		cursor.Close()
		if len(k) < 8 || len(v) > 64 {
			continue
		}
		bn := cscompact.DetectBlockNum(k)
		if bn > 0 && bn < 1<<32 && bn+1 > endBlock {
			endBlock = bn + 1
		}
	}
	if endBlock == 0 {
		return 0, fmt.Errorf("cannot determine end block from tables %v", tables)
	}
	log.Info("Detected end block", "endBlock", endBlock)
	return endBlock, nil
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

func runStorHistBuild(c *cli.Context) error {
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

	stoBuilder := cscompact.NewStorageHistoryBuilder(db, filepath.Join(c.String("datadir"), "chain"))
	return stoBuilder.BuildFromChangesets(ctx, startBlock, endBlock)
}

func runHistoryBuild(c *cli.Context) error {
	erigonDB := c.String("erigon-db")
	datadir := c.String("datadir")
	startBlock := c.Uint64("start")
	endBlock := c.Uint64("end")

	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(erigonDB).Label(kv.ChainDB).Readonly().Accede().
		DBVerbosity(kv.DBVerbosityLvl(2)).
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open erigon mdbx: %w", err)
	}
	defer db.Close()
	log.Info("Erigon MDBX opened READ-ONLY", "path", erigonDB)

	if endBlock == 0 {
		tx, _ := db.BeginRo(context.Background())
		for _, tbl := range []string{
			"AccountChangeSets", "StorageChangeSets",
			"AccountChangeSet", "StorageChangeSet",
		} {
			cursor, err := tx.Cursor(tbl)
			if err != nil {
				continue
			}
			k, v, _ := cursor.Last()
			cursor.Close()
			if len(k) < 8 || len(v) > 64 {
				continue
			}
			bn := cscompact.DetectBlockNum(k)
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

	ctx, cancel := withShutdown()
	defer cancel()

	histDir := filepath.Join(datadir, "chain")

	fromCS := c.Bool("from-changesets")

	log.Info("=== Account History ===", "fromChangesets", fromCS)
	accBuilder := cscompact.NewAccountHistoryBuilder(db, histDir)
	if fromCS {
		if err := accBuilder.BuildFromChangesets(ctx, startBlock, endBlock); err != nil {
			return fmt.Errorf("account history: %w", err)
		}
	} else {
		if err := accBuilder.BuildRange(ctx, startBlock, endBlock); err != nil {
			return fmt.Errorf("account history: %w", err)
		}
	}

	log.Info("=== Storage History ===", "fromChangesets", fromCS)
	stoBuilder := cscompact.NewStorageHistoryBuilder(db, histDir)
	if fromCS {
		return stoBuilder.BuildFromChangesets(ctx, startBlock, endBlock)
	}
	return stoBuilder.BuildRange(ctx, startBlock, endBlock)
}

func runTxLookupBuild(c *cli.Context) error {
	ancientPath := c.String("ancient")
	erigonDB := c.String("erigon-db")
	datadir := c.String("datadir")
	startBlock := c.Uint64("start")
	endBlock := c.Uint64("end")

	outputDir := filepath.Join(datadir, "chain")
	ctx, cancel := withShutdown()
	defer cancel()

	// Reth/Erigon MDBX path — read from TransactionHashNumbers + TransactionBlocks.
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
		log.Info("MDBX opened READ-ONLY for txindex", "path", erigonDB)

		if endBlock == 0 {
			endBlock = detectRethEndBlock(db)
			if endBlock == 0 {
				return fmt.Errorf("cannot detect end block from MDBX")
			}
			log.Info("Detected end block", "endBlock", endBlock)
		}

		builder := txlookup.NewRethBuilder(db, outputDir)
		return builder.BuildRange(ctx, startBlock, endBlock)
	}

	// Geth ancient freezer path.
	if ancientPath == "" {
		return fmt.Errorf("--ancient or --erigon-db is required")
	}
	f, err := freezer.New(ancientPath, 0)
	if err != nil {
		return fmt.Errorf("open input freezer: %w", err)
	}
	defer f.Close()
	log.Info("Input freezer opened", "frozen", f.Frozen())

	if endBlock == 0 {
		endBlock = f.Frozen()
	}

	builder := txlookup.NewSegmentBuilder(f, outputDir)
	return builder.BuildRange(ctx, startBlock, endBlock)
}

func runBodyCompact(c *cli.Context) error {
	ancientPath := c.String("ancient")
	datadir := c.String("datadir")

	f, err := freezer.New(ancientPath, 0)
	if err != nil {
		return fmt.Errorf("open input freezer: %w", err)
	}
	defer f.Close()
	log.Info("Input freezer opened", "frozen", f.Frozen())

	outputDir := filepath.Join(datadir, "chain")
	stage := ethel.NewBodyCompactStage(f, outputDir)

	ctx, cancel := withShutdown()
	defer cancel()
	return stage.Run(ctx)
}

func runHeaderCompact(c *cli.Context) error {
	ancientPath := c.String("ancient")
	datadir := c.String("datadir")

	f, err := freezer.New(ancientPath, 0)
	if err != nil {
		return fmt.Errorf("open input freezer: %w", err)
	}
	defer f.Close()
	log.Info("Input freezer opened", "frozen", f.Frozen())

	outputDir := filepath.Join(datadir, "chain")

	stage := ethel.NewHeaderCompactStage(f, outputDir)

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
	f, err := freezer.New(ancientPath, 0)
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

func runSenderRecovery(c *cli.Context) error {
	ancientPath := c.String("ancient")
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

		outputDir := filepath.Join(datadir, "chain")
		exporter := ethel.NewRethSenderExporter(db, outputDir)
		return exporter.Export(ctx, startBlock, endBlock)
	}

	// Geth ancient path.
	if ancientPath == "" {
		return fmt.Errorf("--ancient or --erigon-db is required")
	}
	workers := c.Int("workers")
	f, err := freezer.New(ancientPath, 0)
	if err != nil {
		return fmt.Errorf("open input freezer: %w", err)
	}
	defer f.Close()
	log.Info("Input freezer opened", "frozen", f.Frozen())

	ancientOut := filepath.Join(datadir, "chain", "freezer")
	if err := os.MkdirAll(ancientOut, 0755); err != nil {
		return err
	}
	of, err := freezer.New(ancientOut, 0)
	if err != nil {
		return fmt.Errorf("open output freezer: %w", err)
	}
	defer of.Close()

	stage := ethel.NewSenderStage(f, of, params.EthereumMainnetChainConfig, workers)
	return stage.Run(ctx)
}

func runRebuildState(c *cli.Context) error {
	ancientPath := c.String("ancient")
	datadir := c.String("datadir")
	leavesDir := c.String("leaves")
	startBlock := c.Uint64("start")
	endBlock := c.Uint64("end")
	verifyInterval := c.Uint64("verify")

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
		DirtySpace(uint64(2 * datasize.GB)).
		DBVerbosity(kv.DBVerbosityLvl(2))
	if hasMDBX {
		mdbxBuilder = mdbxBuilder.Accede()
	}
	db, err := mdbxBuilder.Open(context.Background())
	if err != nil {
		return fmt.Errorf("open mdbx: %w", err)
	}
	defer db.Close()

	ctx, cancel := withShutdown()
	defer cancel()

	// Open Geth input freezer up front for periodic verify and final verify.
	inputF, err := freezer.New(ancientPath, 0)
	if err != nil {
		return fmt.Errorf("open geth ancient: %w", err)
	}
	defer inputF.Close()

	opts := ethel.RebuildOptions{
		StartBlock:     startBlock,
		VerifyInterval: verifyInterval,
		InputFreezer:   inputF,
		ChainConfig:    params.EthereumMainnetChainConfig,
		GethFreezer:    inputF,
	}
	if err := ethel.RebuildStateWith(ctx, db, leavesDir, endBlock, opts); err != nil {
		return err
	}
	ethel.VerifyRebuildRoot(ctx, db, inputF, endBlock)
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

	ctx, cancel := withShutdown()
	defer cancel()

	// Open output freezer (acctcs/storcs) — Reorg reads OLD values from here.
	outFreezer, err := freezer.New(leavesDir, 0)
	if err != nil {
		return fmt.Errorf("open output freezer: %w", err)
	}
	defer outFreezer.Close()

	inputF, err := freezer.New(ancientPath, 0)
	if err != nil {
		return fmt.Errorf("open geth ancient: %w", err)
	}
	defer inputF.Close()

	if err := ethel.Reorg(db, outFreezer, targetBlock); err != nil {
		return fmt.Errorf("reorg: %w", err)
	}
	// VerifyRebuildRoot verifies at endBlock-1, so pass target+1 to verify
	// at the post-unwind head.
	ethel.VerifyRebuildRoot(ctx, db, inputF, targetBlock+1)
	return nil
}

// detectRealTable returns the first table name that has real DupSort data
// (small values), distinguishing from root DBI fallback (large blobs).
func detectRealTable(db kv.RoDB, candidates ...string) string {
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		return candidates[0]
	}
	defer tx.Rollback()
	for _, tbl := range candidates {
		cursor, err := tx.Cursor(tbl)
		if err != nil {
			continue
		}
		k, v, _ := cursor.First()
		cursor.Close()
		if k != nil && len(k) >= 8 && len(v) <= 64 {
			return tbl
		}
	}
	return candidates[0]
}

// detectRethEndBlock finds the highest block number from Reth's TransactionBlocks
// table (last value = last blockNum). Works for both Reth and Erigon MDBX.
func detectRethEndBlock(db kv.RoDB) uint64 {
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		return 0
	}
	defer tx.Rollback()

	// Best source: TransactionBlocks last entry's value = highest blockNum.
	cursor, err := tx.Cursor("TransactionBlocks")
	if err == nil {
		_, v, _ := cursor.Last()
		cursor.Close()
		if len(v) >= 8 {
			bn := binary.LittleEndian.Uint64(v) // Reth stores values in LE
			if bn > 0 && bn < 1<<32 {
				return bn + 1
			}
		}
	}

	// Fallback: scan changeset tables.
	for _, tbl := range []string{
		"AccountChangeSets", "StorageChangeSets",
		"AccountChangeSet", "StorageChangeSet",
	} {
		cursor, err := tx.Cursor(tbl)
		if err != nil {
			continue
		}
		k, _, _ := cursor.Last()
		cursor.Close()
		if len(k) < 8 {
			continue
		}
		bn := cscompact.DetectBlockNum(k)
		if bn > 0 && bn < 1<<32 {
			return bn + 1
		}
	}
	return 0
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
