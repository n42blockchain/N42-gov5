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
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strings"
	"sync"
	"syscall"
	"time"

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
	"github.com/n42blockchain/N42/modules/state"
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
			&cli.StringFlag{Name: "codes-freezer", Usage: "Optional dir with codes.cidx + codes.NNNN.cdat (produced by code-import2fz). Address-indexed bytecode source — works from genesis without an MDBX. Auto-detects <input-headers-bodies>/codes.cidx only when no populated --datadir is supplied."},
			&cli.StringFlag{Name: "senders", Usage: "Optional pre-computed senders freezer dir (avoids ecrecover)"},
			&cli.Uint64Flag{Name: "start", Value: 0, Usage: "Start block (inclusive)"},
			&cli.Uint64Flag{Name: "end", Value: 0, Usage: "End block (exclusive); 0 = all available witness items"},
			&cli.IntFlag{Name: "workers", Value: 8, Usage: "Number of parallel replay workers. The reorder buffer (in-order emit) is heap-bounded by the small channel cap (256); for high worker counts (e.g. 32) ALSO pass --gogc 300 --mem-limit-gb 16 so GC stays infrequent + concurrent and doesn't steal CPU from workers on heavy DeFi blocks (the old >=16 stall was a GC-frequency feedback loop, not a hard limit)."},
			&cli.IntFlag{Name: "readers", Value: 0, Usage: "Parallel input readers for --no-output verification. 0 = auto (up to 6, segment-sharded); output-producing runs remain sequential for ordered cdat writes."},
			&cli.IntFlag{Name: "body-decoders", Value: 0, Usage: "How many readers may expand a whole 8192-block bodyc segment concurrently. 0 = derived ceil(workers/128), which caps input throughput at one single-threaded segment expansion for any run up to 128 workers. Raising it raises input rate and peak RSS together. With --process-shards this is a total budget divided among children."},
			&cli.IntFlag{Name: "process-shards", Value: 1, Usage: "Split an explicit --start/--end verification range across this many child processes. --workers, --readers and --mem-limit-gb are total budgets divided among children. Requires --no-output."},
			&cli.IntFlag{Name: "segment-shard-count", Value: 1, Hidden: true},
			&cli.IntFlag{Name: "segment-shard-index", Value: 0, Hidden: true},
			&cli.IntFlag{Name: "gogc", Value: 0, Usage: "runtime GOGC (debug.SetGCPercent). 0 = Go default (100). For 32-worker runs set ~300 to cut GC frequency."},
			&cli.IntFlag{Name: "mem-limit-gb", Value: 0, Usage: "soft heap ceiling in GiB (debug.SetMemoryLimit). 0 = off. Set ~16 with high --gogc so the heap stays capped and GC stays concurrent (no multi-second STW)."},
			&cli.Float64Flag{Name: "input-high-gb", Value: 0, Usage: "Total decoded-input reservoir high watermark in GiB for --no-output; 0 disables. With --process-shards the budget is divided among children."},
			&cli.Float64Flag{Name: "input-low-gb", Value: 0, Usage: "Total decoded-input reservoir refill watermark in GiB; 0 = half of --input-high-gb. Producers refill only after completed work drains below this level."},
			&cli.BoolFlag{Name: "no-output", Usage: "Skip cdat writes (smoke / throughput tests). Workers still verify gas per block."},
			&cli.BoolFlag{Name: "skip-verify", Usage: "Skip per-block gas verification. Useful when the witness was recorded by a different ProcessBlock version (state-read order drift produces gas mismatches that aren't a framework bug)."},
			&cli.BoolFlag{Name: "continue-on-error", Usage: "Keep replaying past per-block failures (logged + counted). Throughput measurement against a possibly-stale witness needs this; production runs should leave it false so any divergence halts immediately."},
			&cli.BoolFlag{Name: "write-witness", Usage: "Write witness.cdat to the output freezer alongside acctcs/storcs. Off by default — replay typically reads existing witness, so re-emitting it is duplicate work."},
			&cli.BoolFlag{Name: "receipts", Usage: "Opt in to writing receipts.cdat. Off by default — witness-replay's primary outputs are witness + acctcs + storcs; the receipt-copy subcommand owns receipts in chain/freezer/. Per-block receipt-root check still runs regardless of this flag."},
			&cli.StringFlag{Name: "cpu-profile", Usage: "Write a CPU pprof profile directly to this file for the full replay run (does not require the HTTP pprof endpoint)"},
			&cli.StringFlag{Name: "heap-profile", Usage: "Write an end-of-run in-use heap pprof profile directly to this file after a forced GC"},
			&cli.StringFlag{Name: "block-profile", Usage: "Write an end-of-run goroutine blocking profile to this file (enables full block profiling and adds overhead)"},
			&cli.StringFlag{Name: "mutex-profile", Usage: "Write an end-of-run mutex contention profile to this file (enables full mutex profiling and adds overhead)"},
		},
		Action: run,
	}
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(c *cli.Context) error {
	if c.Int("process-shards") > 1 {
		return runProcessShards(c)
	}
	stopProfiles, err := startFileProfiles(
		c.String("cpu-profile"), c.String("heap-profile"),
		c.String("block-profile"), c.String("mutex-profile"),
	)
	if err != nil {
		return err
	}
	defer stopProfiles()

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
	inputHighGB := c.Float64("input-high-gb")
	inputLowGB := c.Float64("input-low-gb")
	if inputHighGB < 0 || inputLowGB < 0 {
		return fmt.Errorf("--input-high-gb and --input-low-gb must be non-negative")
	}
	if inputHighGB == 0 && inputLowGB > 0 {
		return fmt.Errorf("--input-low-gb requires --input-high-gb")
	}
	if inputHighGB > 0 && inputLowGB >= inputHighGB {
		return fmt.Errorf("--input-low-gb must be lower than --input-high-gb")
	}

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

	// Resolve bytecode inputs. An explicit codes-freezer still wins, but a
	// populated MDBX Code table must suppress implicit freezer detection. The
	// freezer is address-indexed and may not represent historical redeploys;
	// silently preferring it over an explicitly supplied content-addressed Code
	// table can make old blocks replay with the wrong bytecode.
	codesDir, hasCodeMDBX, codesAutoDetected, err := resolveCodeInputs(
		hbPath, datadir, c.String("codes-freezer"),
	)
	if err != nil {
		return err
	}
	if codesAutoDetected {
		log.Info("Codes freezer auto-detected", "dir", codesDir)
	}

	// MDBX is now optional: skip when --datadir is empty, or when the
	// codes-freezer is set and the MDBX doesn't exist (avoids the
	// "Accede can't create" failure mode that bit users replaying
	// from-genesis into a fresh output dir).
	logger := log2.New()
	var codeDB kv.RoDB
	if datadir != "" {
		if hasCodeMDBX {
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
		HeadersBodiesPath:   hbPath,
		WitnessPath:         witnessPath,
		OutputPath:          outputPath,
		Datadir:             datadir,
		SendersPath:         sendersPath,
		StartBlock:          start,
		EndBlock:            end,
		Workers:             workers,
		Readers:             c.Int("readers"),
		BodyDecoders:        c.Int("body-decoders"),
		SegmentShardCount:   c.Int("segment-shard-count"),
		SegmentShardIndex:   c.Int("segment-shard-index"),
		InputHighWaterBytes: int64(inputHighGB * (1 << 30)),
		InputLowWaterBytes:  int64(inputLowGB * (1 << 30)),
		NoOutput:            c.Bool("no-output"),
		SkipVerify:          c.Bool("skip-verify"),
		ContinueOnError:     c.Bool("continue-on-error"),
		WriteWitness:        c.Bool("write-witness"),
		WriteReceipts:       c.Bool("receipts"),
		ChainCfg:            chainCfg,
		Engine:              engine,
		CodesFreezerDir:     codesDir,
	}

	log.Info("Witness replay configured",
		"headers/bodies", hbPath,
		"witness", witnessPath,
		"datadir", datadir)
	// Record which allocation mode this run used: a diagnostic run that cannot
	// be told apart from an ordinary one afterwards proves very little.
	if !state.StateObjectPoolingEnabled() {
		log.Info("State object pooling DISABLED (N42_STATE_OBJECT_POOL=off) — " +
			"every newObject is a fresh allocation; this run is a pooling-hypothesis probe")
	}

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

type replayRange struct {
	start uint64
	end   uint64
}

func splitReplayRanges(start, end uint64, shards int) []replayRange {
	ranges := make([]replayRange, shards)
	previous := start
	for i := 1; i <= shards; i++ {
		next := end
		if i < shards {
			ideal := start + (end-start)*uint64(i)/uint64(shards)
			// BODYC is independently compressed at this granularity. Put process
			// boundaries on the nearest segment edge so two processes never decode
			// the same large segment, but only when a shard spans at least two
			// segments. For shorter ranges alignment can create a pathological
			// 90/10 split and costs more in load imbalance than one duplicate decode.
			next = ideal
			if (end-start)/uint64(shards) >= 2*ethel.HeaderSegmentSize {
				next = ((ideal + ethel.HeaderSegmentSize/2) / ethel.HeaderSegmentSize) * ethel.HeaderSegmentSize
			}
			minimum := previous + 1
			maximum := end - uint64(shards-i)
			if next < minimum {
				next = minimum
			}
			if next > maximum {
				next = maximum
			}
		}
		ranges[i-1] = replayRange{start: previous, end: next}
		previous = next
	}
	return ranges
}

func runProcessShards(c *cli.Context) error {
	shards := c.Int("process-shards")
	start, end := c.Uint64("start"), c.Uint64("end")
	if !c.Bool("no-output") {
		return fmt.Errorf("--process-shards requires --no-output (child outputs cannot append to one ordered freezer)")
	}
	if end <= start {
		return fmt.Errorf("--process-shards requires an explicit --end greater than --start")
	}
	for _, name := range []string{"cpu-profile", "heap-profile", "block-profile", "mutex-profile"} {
		if c.String(name) != "" {
			return fmt.Errorf("--process-shards cannot share --%s; profile one child range directly", name)
		}
	}
	if shards > int(end-start) {
		shards = int(end - start)
	}
	totalWorkers := c.Int("workers")
	if totalWorkers <= 0 {
		totalWorkers = runtime.NumCPU()
	}
	if shards > totalWorkers {
		shards = totalWorkers
	}
	totalReaders := c.Int("readers")
	totalBodyDecoders := c.Int("body-decoders")
	totalMemory := c.Int("mem-limit-gb")
	totalInputHigh := c.Float64("input-high-gb")
	totalInputLow := c.Float64("input-low-gb")
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve witness-replay executable: %w", err)
	}
	baseArgs := stripShardOverrides(os.Args[1:])
	ctx, cancel := context.WithCancel(c.Context)
	defer cancel()

	t0 := time.Now()
	log.Info("Process-sharded verification started",
		"shards", shards, "range", fmt.Sprintf("%d-%d", start, end),
		"workers", totalWorkers, "readers", totalReaders, "mem_limit_gb", totalMemory)
	var wg sync.WaitGroup
	errs := make(chan error, shards)
	for i := 0; i < shards; i++ {
		workers := totalWorkers / shards
		if i < totalWorkers%shards {
			workers++
		}
		readers := 0
		if totalReaders > 0 {
			readers = totalReaders / shards
			if i < totalReaders%shards {
				readers++
			}
			if readers < 1 {
				readers = 1
			}
		}
		bodyDecoders := 0
		if totalBodyDecoders > 0 {
			bodyDecoders = totalBodyDecoders / shards
			if i < totalBodyDecoders%shards {
				bodyDecoders++
			}
			if bodyDecoders < 1 {
				bodyDecoders = 1
			}
		}
		memory := 0
		if totalMemory > 0 {
			memory = totalMemory / shards
			if i < totalMemory%shards {
				memory++
			}
			if memory < 1 {
				memory = 1
			}
		}
		args := append([]string{}, baseArgs...)
		args = append(args,
			"--process-shards", "1",
			"--segment-shard-count", fmt.Sprint(shards),
			"--segment-shard-index", fmt.Sprint(i),
			"--start", fmt.Sprint(start), "--end", fmt.Sprint(end),
			"--workers", fmt.Sprint(workers), "--readers", fmt.Sprint(readers),
			"--body-decoders", fmt.Sprint(bodyDecoders),
			"--mem-limit-gb", fmt.Sprint(memory),
			"--input-high-gb", fmt.Sprint(totalInputHigh/float64(shards)),
			"--input-low-gb", fmt.Sprint(totalInputLow/float64(shards)),
		)
		wg.Add(1)
		go func(id int, childArgs []string) {
			defer wg.Done()
			cmd := exec.CommandContext(ctx, executable, childArgs...)
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			if runErr := cmd.Run(); runErr != nil {
				cancel()
				errs <- fmt.Errorf("segment shard %d/%d range %d-%d: %w", id, shards, start, end, runErr)
				return
			}
			errs <- nil
		}(i, args)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	log.Info("Process-sharded verification complete",
		"shards", shards, "blocks", end-start, "elapsed", time.Since(t0).Truncate(time.Millisecond))
	return nil
}

func stripShardOverrides(args []string) []string {
	overrides := map[string]bool{
		"--process-shards":      true,
		"--segment-shard-count": true,
		"--segment-shard-index": true,
		"--start":               true,
		"--end":                 true,
		"--workers":             true,
		"--readers":             true,
		"--body-decoders":       true,
		"--mem-limit-gb":        true,
		"--input-high-gb":       true,
		"--input-low-gb":        true,
	}
	result := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name := arg
		if eq := strings.IndexByte(arg, '='); eq >= 0 {
			name = arg[:eq]
		}
		if !overrides[name] {
			result = append(result, arg)
			continue
		}
		if name == arg && i+1 < len(args) {
			i++
		}
	}
	return result
}

func startFileProfiles(cpuPath, heapPath, blockPath, mutexPath string) (func(), error) {
	var cpuFile, heapFile, blockFile, mutexFile *os.File
	closeProfiles := func() {
		for _, f := range []*os.File{cpuFile, heapFile, blockFile, mutexFile} {
			if f != nil {
				_ = f.Close()
			}
		}
	}
	openProfile := func(path, kind string) (*os.File, error) {
		if path == "" {
			return nil, nil
		}
		f, err := os.Create(path)
		if err != nil {
			return nil, fmt.Errorf("create %s profile: %w", kind, err)
		}
		return f, nil
	}
	var err error
	if cpuPath != "" {
		cpuFile, err = os.Create(cpuPath)
		if err != nil {
			return nil, fmt.Errorf("create CPU profile: %w", err)
		}
		if err := pprof.StartCPUProfile(cpuFile); err != nil {
			cpuFile.Close()
			return nil, fmt.Errorf("start CPU profile: %w", err)
		}
	}
	if heapFile, err = openProfile(heapPath, "heap"); err != nil {
		if cpuFile != nil {
			pprof.StopCPUProfile()
		}
		closeProfiles()
		return nil, err
	}
	if blockFile, err = openProfile(blockPath, "block"); err != nil {
		if cpuFile != nil {
			pprof.StopCPUProfile()
		}
		closeProfiles()
		return nil, err
	}
	if mutexFile, err = openProfile(mutexPath, "mutex"); err != nil {
		if cpuFile != nil {
			pprof.StopCPUProfile()
		}
		closeProfiles()
		return nil, err
	}
	if blockFile != nil {
		runtime.SetBlockProfileRate(1)
	}
	if mutexFile != nil {
		runtime.SetMutexProfileFraction(1)
	}
	return func() {
		if cpuFile != nil {
			pprof.StopCPUProfile()
			if err := cpuFile.Close(); err != nil {
				log.Warn("Close CPU profile", "err", err)
			}
		}
		if heapFile != nil {
			runtime.GC()
			if err := pprof.WriteHeapProfile(heapFile); err != nil {
				log.Warn("Write heap profile", "err", err)
			}
			if err := heapFile.Close(); err != nil {
				log.Warn("Close heap profile", "err", err)
			}
		}
		if blockFile != nil {
			if err := pprof.Lookup("block").WriteTo(blockFile, 0); err != nil {
				log.Warn("Write block profile", "err", err)
			}
			if err := blockFile.Close(); err != nil {
				log.Warn("Close block profile", "err", err)
			}
		}
		if mutexFile != nil {
			if err := pprof.Lookup("mutex").WriteTo(mutexFile, 0); err != nil {
				log.Warn("Write mutex profile", "err", err)
			}
			if err := mutexFile.Close(); err != nil {
				log.Warn("Close mutex profile", "err", err)
			}
		}
	}, nil
}

func resolveCodeInputs(hbPath, datadir, explicitCodesDir string) (codesDir string, hasMDBX, autoDetected bool, err error) {
	if datadir != "" {
		mdbxPath := filepath.Join(datadir, "mdbx.dat")
		info, statErr := os.Stat(mdbxPath)
		switch {
		case statErr == nil:
			if info.IsDir() {
				return "", false, false, fmt.Errorf("--datadir %q has a directory at mdbx.dat", datadir)
			}
			hasMDBX = true
		case !os.IsNotExist(statErr):
			return "", false, false, fmt.Errorf("stat Code MDBX %q: %w", mdbxPath, statErr)
		}
	}

	if explicitCodesDir != "" {
		return explicitCodesDir, hasMDBX, false, nil
	}
	if hasMDBX {
		return "", true, false, nil
	}
	if _, statErr := os.Stat(filepath.Join(hbPath, "codes.cidx")); statErr == nil {
		return hbPath, false, true, nil
	} else if !os.IsNotExist(statErr) {
		return "", false, false, fmt.Errorf("stat codes freezer index: %w", statErr)
	}
	return "", false, false, nil
}
