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
	"runtime"
	"syscall"

	"github.com/c2h5oh/datasize"
	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/internal/ethel"
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
		Usage: "Parallel witness-driven block replay → acctcs/storcs/receipts cdat",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "input-headers-bodies", Usage: "Freezer dir with headers + bodies tables", Required: true},
			&cli.StringFlag{Name: "input-witness", Usage: "Freezer dir with block_witness table (may equal input-headers-bodies)"},
			&cli.StringFlag{Name: "output", Usage: "Freezer dir for acctcs/storcs/receipts/witness output", Required: true},
			&cli.StringFlag{Name: "datadir", Usage: "MDBX datadir holding the Code table (and target for rebuild-state)", Required: true},
			&cli.StringFlag{Name: "senders", Usage: "Optional pre-computed senders freezer dir (avoids ecrecover)"},
			&cli.Uint64Flag{Name: "start", Value: 0, Usage: "Start block (inclusive)"},
			&cli.Uint64Flag{Name: "end", Value: 0, Usage: "End block (exclusive); 0 = all available witness items"},
			&cli.IntFlag{Name: "workers", Value: 32, Usage: "Number of parallel replay workers"},
			&cli.BoolFlag{Name: "no-output", Usage: "Skip cdat writes (smoke / throughput tests). Workers still verify gas per block."},
			&cli.BoolFlag{Name: "skip-verify", Usage: "Skip per-block gas verification. Useful when the witness was recorded by a different ProcessBlock version (state-read order drift produces gas mismatches that aren't a framework bug)."},
			&cli.BoolFlag{Name: "continue-on-error", Usage: "Keep replaying past per-block failures (logged + counted). Throughput measurement against a possibly-stale witness needs this; production runs should leave it false so any divergence halts immediately."},
			&cli.BoolFlag{Name: "write-witness", Usage: "Write witness.cdat to the output freezer alongside receipts/acctcs/storcs. Off by default — replay typically reads existing witness, so re-emitting it is duplicate work."},
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
	workers := c.Int("workers")
	if workers <= 0 {
		workers = runtime.NumCPU()
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

	logger := log2.New()
	codeDB, err := mdbx.NewMDBX(logger).
		Path(datadir).
		Label(kv.ChainDB).
		Accede().
		Readonly().
		MapSize(4 * datasize.TB).
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open Code MDBX: %w", err)
	}
	defer codeDB.Close()

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
		ChainCfg:          chainCfg,
		Engine:            engine,
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
