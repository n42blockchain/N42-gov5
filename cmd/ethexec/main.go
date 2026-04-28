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
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/txlookup"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
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
	modules.N42Init()
	// Merge (not replace) to preserve PlainState and history/index DupSort configs.
	for name, cfg := range modules.N42TableCfg {
		kv.ChaindataTablesCfg[name] = cfg
	}

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
		&cli.StringFlag{Name: "track-addr", Usage: "20-byte address (hex, no 0x) to emit targeted trace logs for"},
		&cli.BoolFlag{Name: "safe-durable", Usage: "Force MDBX default durability (fsync every commit). Default: off — ethexec uses WriteMap+SafeNoSync since the ethel progress marker + state are committed atomically, so crash recovery resumes from the last durable checkpoint"},
		&cli.IntFlag{Name: "gogc", Usage: "Go GC target percentage (higher = less frequent GC, more heap). Default 400 for ethexec forward-replay — 2–4x less GC CPU vs the Go default of 100", Value: 400},
		&cli.Uint64Flag{Name: "dirty-space-mb", Usage: "MDBX dirty page pool size in MB. Larger = fewer forced spills mid-commit; caps at ~commit_interval × per-block dirty size", Value: 2048},
		&cli.Uint64Flag{Name: "cache-account-gb", Usage: "Account S3-FIFO cache budget (GB)", Value: 4},
		&cli.Uint64Flag{Name: "cache-storage-gb", Usage: "Storage S3-FIFO cache budget (GB)", Value: 32},
		&cli.Uint64Flag{Name: "cache-code-gb", Usage: "Code LRU cache budget (GB)", Value: 2},
		&cli.IntFlag{Name: "code-analysis-cache", Usage: "JUMPDEST analysis LRU capacity (entries). Avoids re-running O(n) bytecode scan per contract call", Value: 32768},
		&cli.BoolFlag{Name: "parallel-evm", Usage: "EXPERIMENTAL: use Block-STM parallel EVM for block tx execution. See docs/parallel_evm_plan.md. Default: off (sequential path, same as before)"},
		&cli.IntFlag{Name: "parallel-workers", Usage: "Worker count for --parallel-evm. Default 8 (sweet spot per conflict-analyze data; values >= 16 can stall on heavy contention until Phase 5 adds condvar-based dependency blocking)", Value: 8},
		&cli.IntFlag{Name: "timing-interval", Usage: "Blocks between P50/P99 timing log lines. Default 10000", Value: 10000},
		&cli.BoolFlag{Name: "prefetch-speculative", Usage: "Use reth-style prewarm: speculatively execute block N's txs through a NoopWriter to warm the read LRU with everything internal CALL/SLOAD touches. Falls back to static AccessList path on small blocks (<5 txs) or missing senders. Default true", Value: true},
		&cli.BoolFlag{Name: "prefetch", Usage: "Master switch for the background prefetcher (both static-AL and speculative paths). Set to false to bisect prefetcher-induced LRU races. Default true", Value: true},
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
					&cli.Uint64Flag{Name: "start", Usage: "Start block. Omit to auto-resume from DbInfo/ethel_progress. --start 0 explicitly clears existing PlainState and rebuilds from genesis.", Value: 0},
					&cli.Uint64Flag{Name: "end", Usage: "End block (0=all available)", Value: 0},
					&cli.Uint64Flag{Name: "verify", Usage: "Periodic state-root verify interval (0=verify only at end)", Value: 0},
					&cli.BoolFlag{Name: "evm-from-fallback", Usage: "After any EVM fallback, run EVM for every remaining block instead of trusting downstream freezer changesets"},
					&cli.BoolFlag{Name: "persist-trie", Usage: "After reaching end block, populate HashedAccounts/HashedStorage/TrieOfAccounts/TrieOfStorage so subsequent per-block verify (verify-incremental) runs in O(dirty)"},
					&cli.Uint64Flag{Name: "dirty-space-gb", Usage: "MDBX dirty-page pool size in GB. Default 2. Raise to 32+ for --persist-trie at 10M+ blocks; BootstrapHPH writes 100M+ rows in a single tx and the 2GB default overflows with MDBX_MAP_FULL", Value: 2},
				},
				Action: runRebuildState,
			},
			{
				Name:  "verify-root",
				Usage: "Verify PlainState's state root at a specific block without touching the state. One-shot, read-only. Use after rebuild-state to confirm a checkpoint.",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "ancient", Usage: "Path to Geth ancient chain directory", Required: true},
					&cli.StringFlag{Name: "datadir", Usage: "Path to MDBX with existing PlainState", Required: true},
					&cli.Uint64Flag{Name: "block", Usage: "Block number to verify against", Required: true},
				},
				Action: runVerifyRoot,
			},
			{
				Name:  "db-stats",
				Usage: "Print MDBX table + freezer segment statistics (mirrors `reth db stats`)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "datadir", Usage: "Path to N42 MDBX directory", Required: true},
					&cli.StringFlag{Name: "freezer", Usage: "Override freezer path (default: <datadir>/chain/freezer)"},
					&cli.BoolFlag{Name: "no-mdbx", Usage: "Skip MDBX section"},
					&cli.BoolFlag{Name: "no-freezer", Usage: "Skip freezer section"},
					&cli.BoolFlag{Name: "no-progress", Usage: "Skip progress markers section"},
					&cli.BoolFlag{Name: "hide-empty", Usage: "Hide tables with 0 entries (both freezer and MDBX)"},
					&cli.StringFlag{Name: "sort", Usage: "Sort rows by: size | items | name", Value: "size"},
					&cli.BoolFlag{Name: "with-decoded", Usage: "Sample-estimate decoded element count for list-valued tables (e.g. senders → total tx count). Slower."},
				},
				Action: runDBStats,
			},
			{
				Name:  "verify-incremental",
				Usage: "Per-block HPH state-root verify (requires datadir bootstrapped via rebuild-state --persist-trie). Fast bisect: first MISMATCH block is the bug site.",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "ancient", Usage: "Path to Geth ancient chain directory (for header root comparison)", Required: true},
					&cli.StringFlag{Name: "datadir", Usage: "Path to HPH-bootstrapped MDBX", Required: true},
					&cli.StringFlag{Name: "leaves", Usage: "Path to changeset freezer dir (default: <datadir>/chain/freezer)"},
					&cli.Uint64Flag{Name: "start", Usage: "First block to verify (inclusive). Datadir state must reflect block start-1.", Required: true},
					&cli.Uint64Flag{Name: "end", Usage: "Last block to verify (inclusive)", Required: true},
					&cli.Uint64Flag{Name: "commit-every", Usage: "Commit to MDBX every N blocks", Value: 10000},
					&cli.BoolFlag{Name: "use-incremental", Usage: "EXPERIMENTAL: use incremental HPH (currently broken — produces wrong roots). Default path is legacy full-rebuild per block (correct but slow)."},
				},
				Action: runVerifyIncremental,
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
				Name:  "verify-cs-root",
				Usage: "Rebuild PlainState from changesets to --block, verify state root (no EVM). Use for bisecting CS corruption.",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "ancient", Usage: "Path to Geth ancient chain directory (for header root)", Required: true},
					&cli.StringFlag{Name: "datadir", Usage: "Path to TEMP MDBX (will be created fresh)", Required: true},
					&cli.StringFlag{Name: "leaves", Usage: "Path to changeset freezer dir", Required: true},
					&cli.Uint64Flag{Name: "block", Usage: "Target block to verify root at", Required: true},
				},
				Action: runVerifyCSRoot,
			},
			{
				Name:  "clone-state",
				Usage: "Copy PlainState (Account/Storage/Code) + progress from --src datadir to a fresh --dst (2 TB MapSize). Escapes a stuck MapSize cap without rerunning the 1h+ changeset replay.",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "src", Usage: "Source datadir (read-only, inherits its existing MapSize)", Required: true},
					&cli.StringFlag{Name: "dst", Usage: "Destination datadir (must NOT exist — will be created fresh with 2 TB MapSize)", Required: true},
					&cli.IntFlag{Name: "batch", Usage: "Puts per dst tx commit. Default 500000", Value: 500000},
				},
				Action: runCloneState,
			},
			{
				Name:  "code-import",
				Usage: "Import Bytecodes from Reth MDBX into ethexec Code table + compressed freezer",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "reth-db", Usage: "Path to Reth MDBX (read-only)", Required: true},
					&cli.StringFlag{Name: "target", Usage: "Path to ethexec MDBX(s) to write Code table (comma-separated)", Required: true},
				},
				Action: runCodeImport,
			},
			{
				Name:  "exec-compare",
				Usage: "Offline: apply changesets to shadow MDBX, then compare with primary (no EVM). Run normal ethexec first.",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "primary", Usage: "Path to EVM-executed MDBX (has chain/freezer with changesets)", Required: true},
					&cli.StringFlag{Name: "shadow", Usage: "Path to shadow MDBX (starts at --from state)", Required: true},
					&cli.Uint64Flag{Name: "from", Usage: "First block of CS range to apply", Required: true},
					&cli.Uint64Flag{Name: "to", Usage: "Last block +1 (exclusive) of CS range", Required: true},
				},
				Action: runExecCompare,
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
					for _, tbl := range []string{"Account", "Storage", "HashedAccounts", "HashedStorage"} {
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
	if c.Args().Len() > 0 {
		// Subcommand dispatched — urfave/cli v2 still calls the
		// top-level Action; bail out and let the subcommand run.
		return nil
	}
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

	// Apply GOGC early so Go runtime picks it up for all subsequent
	// allocations. 400 (vs default 100) roughly 2x heap target between
	// GC cycles — at our allocation profile (~200–400 MB churn per
	// commit interval) this cuts GC CPU 60–70% on 128 GB hosts.
	if gogc := c.Int("gogc"); gogc > 0 {
		prev := debug.SetGCPercent(gogc)
		log.Info("Go GC target set", "gogc", gogc, "prev", prev)
	}

	// Enable JUMPDEST analysis cache (Aptos AIP-107 style). Without this
	// every CALL to a contract runs O(n) bytecode scan; profile shows
	// ~4-5% CPU on codeBitmap in forward-replay. 32768-entry LRU covers
	// the active contract working set and caches ~128 MB of bitmaps.
	if cap := c.Int("code-analysis-cache"); cap > 0 {
		vm2.GlobalCodeAnalysisCache = vm2.NewCodeAnalysisCache(cap)
		log.Info("Code analysis cache enabled", "capacity", cap)
	}

	if trackHex := c.String("track-addr"); trackHex != "" {
		trackHex = strings.TrimPrefix(trackHex, "0x")
		raw, err := hex.DecodeString(trackHex)
		if err != nil || len(raw) != 20 {
			return fmt.Errorf("--track-addr must be 20B hex: %v", err)
		}
		var addr types.Address
		copy(addr[:], raw)
		state.SetTrackedAddr(&addr)
		log.Info("Targeted trace enabled", "addr", trackHex)
	}

	// Open Geth ancient freezer.
	log.Info("Opening Geth ancient data", "path", ancientPath)
	f, err := freezer.New(ancientPath, 0)
	if err != nil {
		return fmt.Errorf("open freezer: %w", err)
	}
	defer f.Close()
	log.Info("Freezer opened", "frozen", f.Frozen())

	// Open MDBX with optimized parameters for ETH EL execution.
	// MapSize = 2 TB matches Erigon's default: Windows only MEM_RESERVE
	// the range (no commit charge) since WriteMap is OFF — the actual
	// committed memory follows the real file size.
	// RpAugmentLimit 256*1024 is Reth's fixed value: bounds the
	// reclaimed-list scan depth so GC stays predictable on chain-scale DBs.
	// MergeThreshold 2*8192 matches Erigon default — 4*8192 caused
	// more dirty-page churn per commit.
	// (Coalesce is no longer a flag in libmdbx ≥0.12 — coalescing is
	// automatic in the engine.)
	log.Info("Opening MDBX database", "path", datadir)
	if err := os.MkdirAll(datadir, 0755); err != nil {
		return err
	}
	logger := log2.New()
	dirtySpaceMB := c.Uint64("dirty-space-mb")
	mdbxOpts := mdbx.NewMDBX(logger).
		Path(datadir).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(2 * datasize.TB).
		GrowthStep(2 * datasize.GB).
		WriteMergeThreshold(2 * 8192).
		RpAugmentLimit(256 * 1024).
		LifoReclaim().
		DirtySpace(uint64(datasize.ByteSize(dirtySpaceMB) * datasize.MB)).
		DBVerbosity(kv.DBVerbosityLvl(2))
	if !c.Bool("safe-durable") {
		// WriteMap: MDBX writes directly into the mapped file (no user-space
		// copy), matching Reth's default. SafeNoSync: skip fsync on every
		// commit; safe here because ethel progress + state commit atomically
		// in the same RwTx, so resume-from-crash restarts from the last
		// durable checkpoint without torn state. Expect 15–25% throughput
		// gain on write-heavy forward-replay.
		mdbxOpts = mdbxOpts.WriteMap().SafeNoSync()
		log.Info("MDBX fast mode enabled", "flags", "WriteMap+SafeNoSync+LifoReclaim", "dirty_MB", dirtySpaceMB)
	} else {
		log.Info("MDBX fast mode OFF — durable commits", "flags", "LifoReclaim", "dirty_MB", dirtySpaceMB)
	}
	db, err := mdbxOpts.Open(context.Background())
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
		StartBlock:      startBlock,
		EndBlock:        endBlock,
		CommitInterval:  commitInterval,
		VerifyInterval:  verifyInterval,
		SkipErrors:      skipErrors,
		NoOutputs:       noOutputs,
		ParallelEVM:         c.Bool("parallel-evm"),
		ParallelWorkers:     c.Int("parallel-workers"),
		TimingInterval:      c.Int("timing-interval"),
		PrefetchSpeculative: c.Bool("prefetch-speculative"),
		PrefetchEnabled:     c.Bool("prefetch"),
	}
	if cfg.ParallelEVM {
		log.Warn("EXPERIMENTAL: --parallel-evm enabled (Phase 5 MVP). "+
			"Output freezer writes (receipts/senders/changesets) are SKIPPED "+
			"in this path. See docs/parallel_evm_plan.md",
			"workers", cfg.ParallelWorkers)
	}

	executor := ethel.NewExecutor(f, db, chainCfg, engine, cfg, outFreezer)

	// Apply tunable read-cache budget. Defaults (4/32/2 GB acct/sto/code)
	// are calibrated for a 128 GB host doing forward-replay past block 13M
	// where the storage working set is the dominant read cost.
	acctGB := c.Uint64("cache-account-gb")
	stoGB := c.Uint64("cache-storage-gb")
	codeGB := c.Uint64("cache-code-gb")
	executor.SetCacheBudget(state.CacheBudget{
		AccountBytes: int64(acctGB) << 30,
		StorageBytes: int64(stoGB) << 30,
		CodeBytes:    int64(codeGB) << 30,
	})
	log.Info("Cache budget", "acct_GB", acctGB, "sto_GB", stoGB, "code_GB", codeGB)

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
	evmFromFallback := c.Bool("evm-from-fallback")
	persistTrie := c.Bool("persist-trie")
	startWasSet := c.IsSet("start")
	dirtySpaceGB := c.Uint64("dirty-space-gb")
	if dirtySpaceGB == 0 {
		dirtySpaceGB = 2
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
	if !startWasSet && hasMDBX {
		roTx, rerr := db.BeginRo(context.Background())
		if rerr == nil {
			v, gerr := roTx.GetOne("DbInfo", []byte("ethel_progress"))
			roTx.Rollback()
			if gerr == nil && len(v) == 8 {
				progress := binary.BigEndian.Uint64(v)
				if progress > 0 {
					startBlock = progress + 1
					log.Info("Auto-resuming from DbInfo/ethel_progress",
						"progress", progress, "startBlock", startBlock)
				}
			}
		}
	}

	ctx, cancel := withShutdown()
	defer cancel()

	// Open Geth input freezer up front for periodic verify and final verify.
	inputF, err := freezer.New(ancientPath, 0)
	if err != nil {
		return fmt.Errorf("open geth ancient: %w", err)
	}
	defer inputF.Close()

	opts := ethel.RebuildOptions{
		StartBlock:      startBlock,
		VerifyInterval:  verifyInterval,
		InputFreezer:    inputF,
		ChainConfig:     params.EthereumMainnetChainConfig,
		GethFreezer:     inputF,
		EVMFromFallback: evmFromFallback,
		PersistTrie:     persistTrie,
	}
	if err := ethel.RebuildStateWith(ctx, db, leavesDir, endBlock, opts); err != nil {
		return err
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
	ethel.VerifyRebuildRoot(ctx, db, inputF, verifyEnd)
	return nil
}

func runVerifyRoot(c *cli.Context) error {
	ancientPath := c.String("ancient")
	datadir := c.String("datadir")
	blockNum := c.Uint64("block")

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

	inputF, err := freezer.New(ancientPath, 0)
	if err != nil {
		return fmt.Errorf("open geth ancient: %w", err)
	}
	defer inputF.Close()

	// VerifyRebuildRoot takes endBlock (exclusive), verifies at endBlock-1.
	// Pass blockNum+1 so it verifies AT blockNum.
	ethel.VerifyRebuildRoot(context.Background(), db, inputF, blockNum+1)
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

	inputF, err := freezer.New(ancientPath, 0)
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

	inputF, err := freezer.New(ancientPath, 0)
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
	inputF, err := freezer.New(ancientPath, 0)
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
			ethel.VerifyRebuildRoot(ctx, db, inputF, targetBlock+1)
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

	// Write progress so next run can resume.
	{
		wtx, err := db.BeginRw(ctx)
		if err != nil {
			return err
		}
		if err := ethel.WriteProgress(wtx, targetBlock); err != nil {
			wtx.Rollback()
			return err
		}
		if err := wtx.Commit(); err != nil {
			return err
		}
	}

	// 4. Verify state root at targetBlock via MPT.
	ethel.VerifyRebuildRoot(ctx, db, inputF, endBlock)
	return nil
}

// runCodeImport copies Reth Bytecodes table into one or more ethexec MDBX
// Code tables. Supports comma-separated target paths to populate both
// primary and shadow in one pass.
func runCodeImport(c *cli.Context) error {
	rethPath := c.String("reth-db")
	targets := strings.Split(c.String("target"), ",")

	// Open Reth readonly.
	logger := log2.New()
	rethDB, err := mdbx.NewMDBX(logger).
		Path(rethPath).
		Label(kv.ChainDB).
		MapSize(4 * datasize.TB).
		Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d["Bytecodes"] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open reth: %w", err)
	}
	defer rethDB.Close()

	rtx, err := rethDB.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer rtx.Rollback()

	// Count first.
	cursor, err := rtx.Cursor("Bytecodes")
	if err != nil {
		return err
	}
	var total uint64
	for k, _, e := cursor.First(); k != nil && e == nil; k, _, e = cursor.Next() {
		total++
	}
	cursor.Close()
	log.Info("Reth Bytecodes counted", "entries", total)

	// Open all target MDBX databases.
	var dbs []kv.RwDB
	for _, p := range targets {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if err := os.MkdirAll(p, 0755); err != nil {
			return err
		}
		d, err := mdbx.NewMDBX(logger).
			Path(p).
			Label(kv.ChainDB).
			PageSize(4096).
			MapSize(64 * datasize.GB).
			GrowthStep(2 * datasize.GB).
			DirtySpace(uint64(512 * datasize.MB)).
			DBVerbosity(kv.DBVerbosityLvl(2)).
			Open(context.Background())
		if err != nil {
			return fmt.Errorf("open target %s: %w", p, err)
		}
		dbs = append(dbs, d)
		defer d.Close()
	}

	// Begin write txs on all targets.
	var wtxs []kv.RwTx
	for _, d := range dbs {
		wt, err := d.BeginRw(context.Background())
		if err != nil {
			for _, prev := range wtxs {
				prev.Rollback()
			}
			return err
		}
		wtxs = append(wtxs, wt)
	}

	// Stream copy: Reth Bytecodes → all target Code tables.
	cursor, err = rtx.Cursor("Bytecodes")
	if err != nil {
		for _, wt := range wtxs {
			wt.Rollback()
		}
		return err
	}

	var n uint64
	var totalBytes uint64
	var lastKey []byte
	for k, v, e := cursor.First(); k != nil && e == nil; k, v, e = cursor.Next() {
		for _, wt := range wtxs {
			if err := wt.Put("Code", k, v); err != nil {
				cursor.Close()
				for _, w := range wtxs {
					w.Rollback()
				}
				return fmt.Errorf("put Code: %w", err)
			}
		}
		lastKey = append(lastKey[:0], k...)
		n++
		totalBytes += uint64(len(k) + len(v))
		if n%100_000 == 0 {
			log.Info("  importing", "progress", n, "total", total,
				"dataMB", totalBytes>>20)
		}
		// Commit every 500K to avoid oversized txs.
		if n%500_000 == 0 {
			cursor.Close()
			for _, wt := range wtxs {
				if err := wt.Commit(); err != nil {
					return fmt.Errorf("commit at %d: %w", n, err)
				}
			}
			wtxs = wtxs[:0]
			for _, d := range dbs {
				wt, err := d.BeginRw(context.Background())
				if err != nil {
					return err
				}
				wtxs = append(wtxs, wt)
			}
			// Reopen cursor and seek past last processed key.
			cursor, err = rtx.Cursor("Bytecodes")
			if err != nil {
				for _, wt := range wtxs {
					wt.Rollback()
				}
				return err
			}
			k, v, e = cursor.Seek(lastKey)
			if k != nil && e == nil && bytes.Equal(k, lastKey) {
				k, v, e = cursor.Next() // skip the one we already wrote
			}
			if k == nil {
				break
			}
			continue // re-enter loop with this k,v
		}
	}
	cursor.Close()

	// Final commit.
	for i, wt := range wtxs {
		if err := wt.Commit(); err != nil {
			return fmt.Errorf("final commit target %d: %w", i, err)
		}
	}

	log.Info("Code import complete",
		"entries", n,
		"dataMB", totalBytes>>20,
		"targets", len(dbs))
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
