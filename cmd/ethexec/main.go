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
	"github.com/urfave/cli/v2"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/internal/ethel"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/mmap"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// withShutdown creates a context that cancels on SIGINT/SIGTERM.
// A second signal forces immediate exit.
func withShutdown() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Info("Shutdown signal received — finishing in-flight commit; press Ctrl+C again to force exit")
		cancel()
		<-sig
		log.Warn("Second signal received — forcing immediate exit; freezer/MDBX may be unflushed (recoverable on next start)")
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
		&cli.BoolFlag{Name: "no-outputs", Usage: "Skip writing the output freezer entirely (acctcs, storcs, witness). Receipts are not written by the executor regardless of this flag — run receipt-copy separately to build receipts.cidx. Senders are an INPUT cache only."},
		&cli.BoolFlag{Name: "no-witness", Usage: "Skip ZK witness recording while still emitting acctcs + storcs. Removes one wrapper layer + per-state-read slice append from the hot path. Receipts are not written by the executor either way."},
		&cli.BoolFlag{Name: "pprof", Usage: "Enable mutex/block profiling for pprof flame graphs"},
		&cli.StringFlag{Name: "track-addr", Usage: "20-byte address (hex, no 0x) to emit targeted trace logs for"},
		&cli.BoolFlag{Name: "safe-durable", Usage: "Force MDBX default durability (fsync every commit). Default: off — ethexec uses SafeNoSync since the ethel progress marker + state commit atomically, so crash recovery resumes from the last durable checkpoint"},
		&cli.BoolFlag{Name: "writemap", Usage: "MDBX WriteMap mode (writes go through mmap instead of pwrite). On Linux this is ~15% faster; on Windows it forces every written page into the resident set, causing working-set growth proportional to MDBX file size (~80 GB at 16M blocks) and pagefile thrash. Default: off.", Value: false},
		&cli.IntFlag{Name: "gogc", Usage: "Go GC target percentage (higher = less frequent GC, more heap). Default 100 keeps heap close to live size. Larger values (200-400) reduce GC CPU but inflate Private memory by (1+gogc/100)x — at 64 GB LRU and gogc=400 the runtime over-commits 320 GB, triggering pagefile thrash on 128 GB hosts. Pair with --memory-limit-gb to give the runtime a hard ceiling.", Value: 100},
		&cli.Uint64Flag{Name: "memory-limit-gb", Usage: "Hard upper bound on Go heap (debug.SetMemoryLimit). 0 = no limit. Recommended: physical_RAM * 0.7 (e.g. 90 on 128 GB hosts). Combined with --gogc the runtime adapts: stays at gogc target normally, but tightens GC if heap approaches the limit. Prevents Private-memory blowout that GOGC alone cannot bound.", Value: 0},
		&cli.Uint64Flag{Name: "dirty-space-mb", Usage: "MDBX dirty page pool size in MB. Larger = fewer forced spills mid-commit; caps at ~commit_interval × per-block dirty size", Value: 2048},
		// S3-FIFO budgets are hard caps. 2/4/2 was tuned from the
		// 12h replay pprof: at >5 GB sto cache, hit rate plateaus at
		// ~93% and map probe cost (mapaccess2) crowds out savings.
		// Going beyond 4 GB sto traded MDBX cost for in-process map
		// cost 1:1. Override via flag for memory-rich hosts that
		// want maximum hit rate.
		&cli.Uint64Flag{Name: "cache-account-gb", Usage: "Account S3-FIFO cache budget (GB)", Value: 2},
		&cli.Uint64Flag{Name: "cache-storage-gb", Usage: "Storage S3-FIFO cache budget (GB)", Value: 4},
		&cli.Uint64Flag{Name: "cache-code-gb", Usage: "Code LRU cache budget (GB)", Value: 2},
		&cli.IntFlag{Name: "code-analysis-cache", Usage: "JUMPDEST analysis LRU capacity (entries). Avoids re-running O(n) bytecode scan per contract call", Value: 32768},
		&cli.BoolFlag{Name: "parallel-evm", Usage: "EXPERIMENTAL: use Block-STM parallel EVM for block tx execution. See docs/parallel_evm_plan.md. Default: off (sequential path, same as before). NOTE: intra-block parallelism only pays when execution is SINGLE-STREAM (live/tip-following, single sequential replay); do NOT combine with block-level parallel pipelines (e.g. witness-replay --workers N) that already saturate the cores — stacking the two oversubscribes and slows both"},
		&cli.IntFlag{Name: "parallel-workers", Usage: "Worker count for --parallel-evm. Default 8 (sweet spot per conflict-analyze data; values >= 16 can stall on heavy contention until Phase 5 adds condvar-based dependency blocking)", Value: 8},
		&cli.IntFlag{Name: "timing-interval", Usage: "Blocks between P50/P99 timing log lines. Default 10000", Value: 10000},
		&cli.BoolFlag{Name: "prefetch-speculative", Usage: "Use reth-style prewarm: speculatively execute block N's txs through a NoopWriter to warm the read LRU with everything internal CALL/SLOAD touches. Falls back to static AccessList path on small blocks (<5 txs) or missing senders. Default true", Value: true},
		&cli.BoolFlag{Name: "prefetch", Usage: "Master switch for the background prefetcher (both static-AL and speculative paths). Set to false to bisect prefetcher-induced LRU races. Default true", Value: true},
		&cli.BoolFlag{Name: "prewarm-al", Usage: "Main-thread synchronous AccessList prewarm at start of every block. Race-free replacement for the prefetcher's removed storage LRU writes. Default true.", Value: true},
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
				Name:  "bundle-hash",
				Usage: "Generate manifest for a minimal-client bundle (server side; publish after state-root verify). Default hash: blake3-256.",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "datadir", Usage: "Bundle root directory (e.g. dir containing chain/freezer/headerc.cidx, or a flat archive dir with --include-all)", Required: true},
					&cli.StringFlag{Name: "out", Usage: "Output manifest path (.json)", Required: true},
					&cli.Uint64Flag{Name: "chain-id", Usage: "Chain ID for manifest header", Value: 1},
					&cli.Uint64Flag{Name: "block-start", Usage: "Block range start", Value: 0},
					&cli.Uint64Flag{Name: "block-end", Usage: "Block range end (inclusive)", Value: 0},
					&cli.IntFlag{Name: "workers", Usage: "Concurrent file hashers (0=GOMAXPROCS)", Value: 0},
					&cli.StringFlag{Name: "algo", Usage: "Hash algorithm: blake3-256 (default) or blake2b-256 (legacy)", Value: ""},
					&cli.BoolFlag{Name: "include-all", Usage: "Match every regular file under root (drops the chain/freezer requirement). Use for flat archive dirs (snapshot/, history/) that don't follow the freezer layout."},
				},
				Action: runBundleHash,
			},
			{
				Name:  "bundle-verify",
				Usage: "Verify a downloaded bundle against its blake2b manifest (client side)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "datadir", Usage: "Bundle root directory", Required: true},
					&cli.StringFlag{Name: "manifest", Usage: "Path to manifest .json (typically alongside the bundle)", Required: true},
					&cli.IntFlag{Name: "workers", Usage: "Concurrent file verifiers (0=GOMAXPROCS)", Value: 0},
				},
				Action: runBundleVerify,
			},
			{
				Name:  "sender-recovery",
				Usage: "Extract senders from Geth ancient, N42 columnar bodyc, or Reth MDBX",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "ancient", Usage: "Path to Geth ancient chain directory"},
					&cli.StringFlag{Name: "bodyc", Usage: "Path to N42 columnar dir (has headerc.cidx + bodyc.cidx). Use when no geth ancient available — minimal-client bundle path."},
					&cli.StringFlag{Name: "erigon-db", Usage: "Path to Reth/Erigon MDBX (reads TransactionSenders)"},
					&cli.StringFlag{Name: "datadir", Usage: "Path to output directory", Required: true},
					&cli.IntFlag{Name: "workers", Usage: "Number of parallel workers (ecrecover mode only)", Value: 0},
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
					&cli.IntFlag{Name: "frame-blocks", Value: -1, Usage: "Blocks per sub-segment frame; -1 keeps the built-in default (256), 0 emits the legacy whole-segment layout"},
				},
				Action: runHeaderCompact,
			},
			{
				Name:  "body-compact",
				Usage: "Columnar-compress bodies from Geth ancient (8192-block segments, freezer-style multi-file)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "ancient", Usage: "Path to Geth ancient chain directory", Required: true},
					&cli.StringFlag{Name: "datadir", Usage: "Path to output directory", Required: true},
					&cli.IntFlag{Name: "frame-blocks", Value: -1, Usage: "Blocks per sub-segment frame; -1 keeps the built-in default (256), 0 emits the legacy whole-segment layout"},
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
					&cli.Uint64Flag{Name: "dirty-space-gb", Usage: "MDBX dirty-page pool size in GB. Default 2. Raise to 32+ for --persist-trie at 10M+ blocks; BootstrapHPH writes 100B+ rows in a single tx and the 2GB default overflows with MDBX_MAP_FULL", Value: 2},
					&cli.StringFlag{Name: "wipes-sidecar", Usage: "Optional freezer dir containing a wipes.cdat sidecar produced by storcs-extract-wipes. Required when rebuilding from witness-replay output, which lacks SELFDESTRUCT pre-wipe entries. Auto-detects <leaves>/wipes.cidx if not specified."},
				&cli.BoolFlag{Name: "skip-final-verify", Usage: "Skip the post-rebuild HPH state-root verify (~5h on 25M state). Trust shifts to upstream bundle hash (see minimal-client design — server runs verify once when producing bundle; multiple servers reproducing the same blake2b are the trustless anchor)."},
					// --track-addr is defined at the app level (execFlags); see run()
					// for the SetTrackedAddr wiring. urfave/cli treats app-level flags
					// as global so subcommands can read them via c.String("track-addr")
					// without a redeclaration here. (Note: rebuild-state's apply path
					// goes via leaves journal → direct MDBX writes, NOT through
					// BufferedPlainStateWriter / IBS, so [TRACK] hooks never fire.
					// Use the top-level forward-execution path to debug zombie bugs.)
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
					&cli.IntFlag{Name: "workers", Usage: "Parallel re-hash workers (per-worker RoTx over disjoint addr ranges); 0=NumCPU capped at 16, 1=sequential", Value: 0},
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
					&cli.StringFlag{Name: "ancient", Usage: "Path to Geth ancient chain directory (for header root). Optional in --no-header mode."},
					&cli.Uint64Flag{Name: "block", Usage: "Block number to verify (cosmetic in --no-header mode)"},
					&cli.StringFlag{Name: "mode", Usage: "mpt | hph | both", Value: "both"},
					&cli.BoolFlag{Name: "concurrent", Usage: "Use ConcurrentMPTRootComputer (16-way parallel HPH) for the hph path", Value: false},
					&cli.StringFlag{Name: "tmpdir", Usage: "Directory for ETL sort-merge tmpfiles (mode=mpt). Default uses system TEMP — set this to a path on the same drive as --datadir if C:\\ has limited space (~17 GB needed at 17M-block scale)"},
					&cli.Uint64Flag{Name: "dirty-space-gb", Usage: "MDBX dirty-page pool size in GB. Default 32 (was 2). Bulk-load via ETL writes 240M+ entries in a single tx; 2 GB overflows with MDBX_MAP_FULL.", Value: 32},
					&cli.BoolFlag{Name: "no-header", Usage: "Skip the geth-ancient header lookup and the match-against-header check. Useful for one-shot HPH bulk-rebuilds (lazy CommitmentBranches population) when no geth ancient is available."},
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
				Name:  "txlookup-query",
				Usage: "Query / self-verify txlookup segments (tx hash -> block number)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "dir", Usage: "Segment directory (holds txindex.cidx/cdat)", Required: true},
					&cli.StringFlag{Name: "tx", Usage: "Single tx hash (hex) to look up"},
					&cli.StringFlag{Name: "ancient", Usage: "Geth ancient dir: self-verify sampled txs against real bodies"},
					&cli.StringFlag{Name: "blocks", Usage: "Comma-separated block numbers to sample for self-verify"},
				},
				Action: runTxLookupQuery,
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

	// Apply GOGC + GOMEMLIMIT early so Go runtime picks them up for all
	// subsequent allocations. GOGC sets the soft heap target (live × (1 +
	// gogc/100) before GC), GOMEMLIMIT is a hard upper bound that
	// tightens GC adaptively as heap approaches it. Use both together:
	// GOGC for steady-state efficiency, GOMEMLIMIT for OOM safety.
	if gogc := c.Int("gogc"); gogc > 0 {
		prev := debug.SetGCPercent(gogc)
		log.Info("Go GC target set", "gogc", gogc, "prev", prev)
	}
	if memLimitGB := c.Uint64("memory-limit-gb"); memLimitGB > 0 {
		limit := int64(memLimitGB) << 30
		debug.SetMemoryLimit(limit)
		log.Info("Go memory limit set", "limit_GB", memLimitGB)
	} else if phys := mmap.TotalMemory(); phys > 0 {
		// Auto-default heap target to 70% of phys RAM (leaves headroom for MDBX + OS).
		limit := int64(phys * 70 / 100)
		debug.SetMemoryLimit(limit)
		log.Info("Go memory limit auto-set", "limit_GB", uint64(limit)>>30, "phys_GB", phys>>30)
	}
	// Sanity-check cache budget vs effective heap target, log so the user
	// sees what they signed up for. Heap peak ≈ cache_total × (1 + gogc/100)
	// + ~2 GB runtime overhead. On a 128 GB host with 30-50 GB MDBX mmap
	// allow for, target peak ≤ 80 GB to leave OS headroom.
	{
		cacheTotal := c.Uint64("cache-account-gb") + c.Uint64("cache-storage-gb") + c.Uint64("cache-code-gb")
		gogc := c.Int("gogc")
		heapPeak := float64(cacheTotal) * (1.0 + float64(gogc)/100.0)
		log.Info("Memory budget",
			"cache_GB", cacheTotal,
			"gogc", gogc,
			"heap_peak_GB", fmt.Sprintf("%.0f", heapPeak),
			"memory_limit_GB", c.Uint64("memory-limit-gb"))
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

	// Open input source. Two layouts supported:
	//
	//   - Geth ancient (raw RLP, headers.NNNN.cdat / bodies.NNNN.cdat
	//     with 64-block batches). freezer.New auto-downgrades to RO if
	//     the dir looks like geth's chaindata.
	//   - N42 columnar (headerc.NNNN.cdat / bodyc.NNNN.cdat with 8192-block
	//     segments). Detected by headerc.cidx; opened via compact readers
	//     and wired into the executor as the primary header/body source.
	//
	// Both paths still open freezer.New so receipts.cdat can be read for
	// the gas-mismatch diagnostic dump. With N42 columnar, the open is
	// best-effort: if headers.cidx isn't there, freezer.New still opens
	// available extended tables and returns; we just use compact readers
	// for headers/bodies.
	// HARD RULE: --ancient is INPUT data — open read-only. Even if the
	// dir is N42's own format, RW open triggers freezer.New's table-
	// alignment truncate path, which wiped a 144 MB senders.cidx down
	// to 24 KB on 2026-05-05. Anything that needs to write goes to
	// --datadir, never --ancient.
	log.Info("Opening input data (read-only)", "path", ancientPath)
	f, err := freezer.NewReadOnly(ancientPath)
	if err != nil {
		return fmt.Errorf("open freezer: %w", err)
	}
	defer f.Close()

	var compactHR *ethel.HeaderCompactReader
	var compactBR *ethel.BodyCompactReader
	var compactHRPrefetch *ethel.HeaderCompactReader
	var compactBRPrefetch *ethel.BodyCompactReader
	if _, err := os.Stat(filepath.Join(ancientPath, "headerc.cidx")); err == nil {
		hr, err := ethel.OpenHeaderCompact(ancientPath)
		if err != nil {
			return fmt.Errorf("open headerc: %w", err)
		}
		br, err := ethel.OpenBodyCompact(ancientPath)
		if err != nil {
			hr.Close()
			return fmt.Errorf("open bodyc: %w", err)
		}
		compactHR = hr
		compactBR = br
		defer compactHR.Close()
		defer compactBR.Close()
		// Separate reader instances for the prefetcher. The compact
		// readers cache the most-recently-decoded segment in a single
		// non-thread-safe field, so the executor and prefetcher fighting
		// over the same instance triggers a decode-per-read on every
		// access (~500 ms each → 1.6 blk/s). Two instances cost an
		// extra fd + ~100 KB cache but eliminate the contention.
		hr2, err := ethel.OpenHeaderCompact(ancientPath)
		if err == nil {
			br2, err2 := ethel.OpenBodyCompact(ancientPath)
			if err2 == nil {
				compactHRPrefetch = hr2
				compactBRPrefetch = br2
				defer compactHRPrefetch.Close()
				defer compactBRPrefetch.Close()
			} else {
				hr2.Close()
			}
		}
		log.Info("Input headers/bodies", "format", "n42-columnar",
			"path", ancientPath, "max_block", compactHR.MaxBlock())
	} else {
		log.Info("Input headers/bodies", "format", "geth-ancient",
			"path", ancientPath, "frozen", f.Frozen())
	}

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
	useWriteMap, useSafeNoSync, flagsLog := selectMDBXFlags(c.Bool("writemap"), c.Bool("safe-durable"))
	if useSafeNoSync {
		mdbxOpts = mdbxOpts.SafeNoSync()
	}
	if useWriteMap {
		mdbxOpts = mdbxOpts.WriteMap()
	}
	log.Info("MDBX flags", "flags", flagsLog, "dirty_MB", dirtySpaceMB)
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
		NoWitness:       c.Bool("no-witness"),
		ParallelEVM:         c.Bool("parallel-evm"),
		ParallelWorkers:     c.Int("parallel-workers"),
		TimingInterval:      c.Int("timing-interval"),
		PrefetchSpeculative: c.Bool("prefetch-speculative"),
		PrefetchEnabled:     c.Bool("prefetch"),
		PrewarmAccessList:   c.Bool("prewarm-al"),
	}
	if cfg.ParallelEVM {
		log.Warn("EXPERIMENTAL: --parallel-evm enabled (Phase 5 MVP). "+
			"Output freezer writes (receipts/senders/changesets) are SKIPPED "+
			"in this path. See docs/parallel_evm_plan.md",
			"workers", cfg.ParallelWorkers)
	}

	executor := ethel.NewExecutor(f, db, chainCfg, engine, cfg, outFreezer)
	if compactHR != nil && compactBR != nil {
		executor.SetCompactReaders(compactHR, compactBR)
		if compactHRPrefetch != nil && compactBRPrefetch != nil {
			executor.SetPrefetchCompactReaders(compactHRPrefetch, compactBRPrefetch)
		}
	}

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
	// Fallback: SegmentStore format. Check the canonical chain/freezer/
	// location first (commit cd0b0d87 moved all freezer-format output here),
	// then chain/ for any legacy datadirs, then the input ancient as a final
	// fallback.
	if !senderFound {
		for _, dir := range []string{filepath.Join(datadir, "chain", "freezer"), filepath.Join(datadir, "chain"), ancientPath} {
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
	// Fallback: senders as a freezer table at the input dir (the format
	// produced by `ethexec sender-recovery --ancient`). Common when the
	// input dir holds N42 columnar headers/bodies plus a sibling
	// senders.cidx + senders.NNNN.cdat in standard freezer-batch format.
	if !senderFound {
		if tbl := f.Table("senders"); tbl != nil && tbl.Items() > 0 {
			executor.SetSenderFreezer(f)
			log.Info("Pre-computed senders loaded (input freezer)", "path", ancientPath, "items", tbl.Items())
			senderFound = true
		}
	}
	if !senderFound {
		log.Info("No pre-computed senders, using ecrecover from signatures")
	}

	// Auto-wire codes.cdat as a bytecode source. Probed in two places
	// in preference order: the configured ancient/input dir (where a
	// bundle ships codes.cidx alongside headerc/bodyc) and the output
	// freezer dir (legacy: code-import2fz wrote into <datadir>/chain/
	// freezer/codes.cidx). First hit wins.
	codesProbes := []string{ancientPath, outFreezerPath}
	for _, dir := range codesProbes {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "codes.cidx")); err != nil {
			continue
		}
		r, err := ethel.NewCodesFreezerReader(dir)
		if err != nil {
			log.Warn("codes.cidx present but failed to open", "dir", dir, "err", err)
			break
		}
		executor.SetCodesFreezer(r)
		log.Info("Codes freezer loaded — MDBX Code table will be consulted only on miss",
			"dir", dir, "contracts", r.Items())
		break
	}

	// (Compact reader auto-wiring moved up next to freezer.New so it
	// runs before any code path that might depend on Frozen() / Ancient
	// from a partial geth ancient.)

	ctx, cancel := withShutdown()
	defer cancel()
	return executor.Run(ctx)
}

// selectMDBXFlags maps the CLI choices to the concrete MDBX flag set
// applied at env-open time. Pure function; the goal is to pin the
// default policy in tests so a future "let's flip WriteMap back on"
// edit lights up the regression that caused the Windows OOM at 16M
// blocks (mmap'd dirty pages reluctant to be reclaimed by the kernel
// → WS grew to MDBX-file-size, ~80 GB).
func selectMDBXFlags(writeMap, safeDurable bool) (useWriteMap, useSafeNoSync bool, label string) {
	label = "LifoReclaim"
	useSafeNoSync = !safeDurable
	if useSafeNoSync {
		label = "SafeNoSync+" + label
	}
	useWriteMap = writeMap
	if useWriteMap {
		label = "WriteMap+" + label
	}
	return
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

// keccak256 returns the legacy-Keccak256 hash of b (32 bytes). Code-import
// uses it to verify codeHash == keccak(decoded_bytecode) before writing.
func keccak256(b []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(b)
	return h.Sum(nil)
}

// ethelBodyTxHashes decodes a geth-format stored body into its transaction
// hashes. Supplied to the txindex builder by the caller so internal/txlookup
// does not have to import internal/ethel (which would close an import cycle
// with the root internal package's live index).
func ethelBodyTxHashes(bodyData []byte) ([]types.Hash, error) {
	body, err := ethel.DecodeGethBody(bodyData)
	if err != nil {
		return nil, err
	}
	out := make([]types.Hash, len(body.Transactions))
	for i, tx := range body.Transactions {
		out[i] = tx.Hash()
	}
	return out, nil
}
