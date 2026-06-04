// witness-replay: parallel witness-driven block replay.
//
// Reads headers + bodies + block_witness from input freezer cdat files,
// re-executes each block in parallel against a witness-backed
// StateReader (code from MDBX), verifies receipts root + gasUsed, and
// emits acctcs / storcs / receipts cdat output. After replay the user
// runs ethexec rebuild-state on the same datadir to populate MDBX
// PlainState — splitting replay (parallel, CPU-bound) from state apply
// (sequential, MDBX-bound) keeps both phases simple.
//
// Phase A in the floating-giggling-thompson plan; Phase B (catch-up
// past the witness range) and Phase C (Engine API chain follow) reuse
// existing ethexec / engine_api code paths.
package main

import (
	"context"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"syscall"

	"github.com/c2h5oh/datasize"
	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/metrics"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/params"
)

func main() {
	modules.N42Init()
	for name, cfg := range modules.N42TableCfg {
		kv.ChaindataTablesCfg[name] = cfg
	}

	// pprof on :6061 (avoids :6060 ethexec collision).
	go func() { _ = http.ListenAndServe("localhost:6061", nil) }()

	app := &cli.App{
		Name:  "witness-replay",
		Usage: "Parallel witness-driven block replay → acctcs/storcs cdat (+ optional witness, --receipts)",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "input-headers-bodies", Usage: "Freezer dir with headers + bodies tables", Required: true},
			&cli.StringFlag{Name: "input-witness", Usage: "Freezer dir with block_witness table (may equal input-headers-bodies)"},
			&cli.StringFlag{Name: "output", Usage: "Freezer dir for acctcs + storcs (+ optional witness with --write-witness, + receipts with --receipts)", Required: true},
			&cli.StringFlag{Name: "datadir", Usage: "MDBX datadir holding the Code table (and target for rebuild-state). Optional when --codes-freezer is provided."},
			&cli.StringFlag{Name: "codes-freezer", Usage: "Optional dir with codes.cidx + codes.NNNN.cdat (produced by code-import2fz). Address-indexed bytecode source — works from genesis without an MDBX. Auto-detects <input-headers-bodies>/codes.cidx if not specified."},
			&cli.StringFlag{Name: "senders", Usage: "Optional pre-computed senders freezer dir (avoids ecrecover)"},
			&cli.Uint64Flag{Name: "start", Value: 0, Usage: "Start block (inclusive)"},
			&cli.Uint64Flag{Name: "end", Value: 0, Usage: "End block (exclusive); 0 = all available witness items"},
			&cli.IntFlag{Name: "workers", Value: 8, Usage: "Number of parallel replay workers. The reorder buffer (in-order emit) is heap-bounded by the small channel cap (256); for high worker counts (e.g. 32) ALSO pass --gogc 300 --mem-limit-gb 16 so GC stays infrequent + concurrent and doesn't steal CPU from workers on heavy DeFi blocks (the old >=16 stall was a GC-frequency feedback loop, not a hard limit)."},
			&cli.IntFlag{Name: "gogc", Value: 0, Usage: "runtime GOGC (debug.SetGCPercent). 0 = Go default (100). For 32-worker runs set ~300 to cut GC frequency."},
			&cli.IntFlag{Name: "mem-limit-gb", Value: 0, Usage: "soft heap ceiling in GiB (debug.SetMemoryLimit). 0 = off. Set ~16 with high --gogc so the heap stays capped and GC stays concurrent (no multi-second STW)."},
			&cli.BoolFlag{Name: "no-output", Usage: "Skip cdat writes (smoke / throughput tests). Workers still verify gas per block."},
			&cli.BoolFlag{Name: "skip-verify", Usage: "Skip per-block gas verification. Useful when the witness was recorded by a different ProcessBlock version (state-read order drift produces gas mismatches that aren't a framework bug)."},
			&cli.BoolFlag{Name: "continue-on-error", Usage: "Keep replaying past per-block failures (logged + counted). Throughput measurement against a possibly-stale witness needs this; production runs should leave it false so any divergence halts immediately."},
			&cli.BoolFlag{Name: "write-witness", Usage: "Write witness.cdat to the output freezer alongside acctcs/storcs. Off by default — replay typically reads existing witness, so re-emitting it is duplicate work."},
			&cli.BoolFlag{Name: "receipts", Usage: "Opt in to writing receipts.cdat. Off by default — witness-replay's primary outputs are witness + acctcs + storcs; the receipt-copy subcommand owns receipts in chain/freezer/. Per-block receipt-root check still runs regardless of this flag."},
		},
		Action: run,
	}
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(c *cli.Context) error {
	hbPath := c.String("input-headers-bodies")
	witnessPath := c.String("input-witness")
	if witnessPath == "" {
		witnessPath = hbPath
	}
	outputPath := c.String("output")
	datadir := c.String("datadir")
	sendersPath := c.String("senders")
	start := c.Uint64("start")
	end := c.Uint64("end")

	// --senders defaults to auto-detect. A pre-computed senders freezer skips
	// ecrecover, which profiling showed was ~49% of replay CPU (secp256k1
	// ext_ecdsa_recover via cgo). Look beside the witness, then under the
	// datadir's freezer, then the datadir itself. An explicit --senders wins.
	if sendersPath == "" {
		for _, cand := range []string{witnessPath, filepath.Join(datadir, "chain", "freezer"), datadir} {
			if cand == "" {
				continue
			}
			if _, err := os.Stat(filepath.Join(cand, "senders.cidx")); err == nil {
				sendersPath = cand
				log.Info("Senders freezer auto-detected (skips ecrecover ~49% CPU)", "dir", sendersPath)
				break
			}
		}
	}

	workers := c.Int("workers")
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	// GC tuning — the in-order aggregator buffers out-of-order results, so at
	// high worker counts a heavy DeFi block makes GC run often and steal worker
	// CPU (mark-assist). A higher GOGC cuts GC frequency. Default: bump to 300
	// automatically for >=16 workers; an explicit --gogc always wins. A soft
	// memory limit (--mem-limit-gb) keeps the heap capped so GC stays
	// concurrent; left off by default (host-RAM dependent).
	if g := c.Int("gogc"); g > 0 {
		debug.SetGCPercent(g)
	} else if workers >= 16 {
		debug.SetGCPercent(300)
	}
	if m := c.Int("mem-limit-gb"); m > 0 {
		debug.SetMemoryLimit(int64(m) << 30)
	}

	// JUMPDEST analysis cache (lock-free Get path). Skipping the LRU
	// promote-on-Get under high parallelism removed the contention
	// that made this cache slower than recomputing pre-fix.
	vm2.GlobalCodeAnalysisCache = vm2.NewCodeAnalysisCache(65536)

	// Bytecode cache: skip MDBX CGo round-trip per ReadAccountCode.
	// Profile showed cgocall + stdcall2 ~7% pre-cache; bytecodes are
	// immutable (content-addressed by codeHash) so any cache hit is
	// guaranteed-correct. 32K entries × ~12KB avg ≈ 400MB worst case.
	ethel.GlobalBytecodeCache = ethel.NewBytecodeCache(32768)

	// Throughput tool: disable the per-EVM-call global counters (all workers
	// Inc the same counter → cache-line contention, ~3.5% CPU). Set before any
	// worker spawns; the node leaves these on.
	metrics.EVMHotMetricsEnabled = false

	// Resolve codes-freezer: explicit flag wins; otherwise auto-detect
	// <hbPath>/codes.cidx.
	codesDir := c.String("codes-freezer")
	if codesDir == "" {
		if _, err := os.Stat(filepath.Join(hbPath, "codes.cidx")); err == nil {
			codesDir = hbPath
			log.Info("Codes freezer auto-detected", "dir", codesDir)
		}
	}

	// MDBX is now optional: skip when --datadir is empty, or when the
	// codes-freezer is set and the MDBX doesn't exist (avoids the
	// "Accede can't create" failure mode that bit users replaying
	// from-genesis into a fresh output dir).
	logger := log2.New()
	var codeDB kv.RoDB
	if datadir != "" {
		_, statErr := os.Stat(filepath.Join(datadir, "mdbx.dat"))
		if statErr == nil {
			db, err := mdbx.NewMDBX(logger).
				Path(datadir).
				Label(kv.ChainDB).
				Accede().
				Readonly().
				MapSize(4 * datasize.TB).
				Open(context.Background())
			if err != nil {
				return fmt.Errorf("open Code MDBX: %w", err)
			}
			defer db.Close()
			codeDB = db
		} else if codesDir == "" {
			return fmt.Errorf("--datadir %q has no mdbx.dat and no --codes-freezer was provided; supply one or the other for bytecode lookup", datadir)
		} else {
			log.Info("MDBX absent at --datadir; using codes-freezer only", "datadir", datadir)
		}
	}

	chainCfg := params.EthereumMainnetChainConfig
	engine := ethel.NewEthReplayEngine(chainCfg)

	cfg := ethel.WitnessReplayConfig{
		HeadersBodiesPath: hbPath,
		WitnessPath:       witnessPath,
		OutputPath:        outputPath,
		Datadir:           datadir,
		SendersPath:       sendersPath,
		StartBlock:        start,
		EndBlock:          end,
		Workers:           workers,
		NoOutput:          c.Bool("no-output"),
		SkipVerify:        c.Bool("skip-verify"),
		ContinueOnError:   c.Bool("continue-on-error"),
		WriteWitness:      c.Bool("write-witness"),
		WriteReceipts:     c.Bool("receipts"),
		ChainCfg:          chainCfg,
		Engine:            engine,
		CodesFreezerDir:   codesDir,
	}

	log.Info("Witness replay configured",
		"headers/bodies", hbPath,
		"witness", witnessPath,
		"datadir", datadir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Info("Shutdown signal received — finishing in-flight blocks; press Ctrl+C again to force exit")
		cancel()
		<-sig
		log.Warn("Second signal received — forcing immediate exit; output cdat may be partial (re-run on next start picks up where we left off)")
		os.Exit(1)
	}()
	if err := ethel.RunWitnessReplay(ctx, cfg, codeDB); err != nil {
		return err
	}

	if !c.Bool("no-output") {
		log.Info("Next: rebuild PlainState from cdat output",
			"cmd", fmt.Sprintf(
				"build/bin/ethexec.exe rebuild-state --ancient %s --datadir %s --leaves %s --verify 0",
				hbPath, datadir, outputPath))
	}
	return nil
}
