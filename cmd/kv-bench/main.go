// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// kv-bench: pluggable KV-engine microbenchmark for blockchain
// workloads.
//
// First version: MDBX 4 KiB vs 16 KiB vs 32 KiB pages, on the
// workloads that match N42's actual access pattern. Pebble / Badger
// backends added in follow-up commits.
//
// Usage:
//
//	build/bin/kv-bench --backend=mdbx --page-kib=16 --workload=state_sim --keys=1000000 --datadir=/tmp/kvb
//	build/bin/kv-bench --backend=mdbx --page-kib=4  --workload=state_sim --keys=1000000 --datadir=/tmp/kvb
//	... compare CSV output
//
// The benchmark machine should match production:
//   - Ryzen 9 9950X / 128 GiB DDR5 / Samsung 990 Pro NVMe
//   - go1.25 (matches the production binary)
//   - run on a clean tmpdir each time so the engine is not warm
//
// Workloads (see workloads.go):
//   - random_write: N inserts with random 32 B keys
//   - random_read_warm: read keys just written (cache-warm)
//   - random_read_cold: read keys NEVER written (forces MDBX → disk)
//   - range_scan: cursor.First/Next over the whole table
//   - state_sim: simulate one ApplyTo commit (700K acct + 1.5M storage,
//     sorted keys, cursor.Append; the closest match to production
//     bg-flush behavior)

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
)

const (
	// All benchmarks operate on a single test table to keep the
	// MDBX setup minimal. Real production has dozens of tables; the
	// engine-level perf characteristics shown here are still
	// representative because table count affects overhead, not
	// throughput-per-table.
	benchTable = "BenchTable"
)

func main() {
	_ = log.Root()
	logger := log2.New()

	var (
		backend     = flag.String("backend", "mdbx", "KV backend: mdbx (only one supported in this version)")
		datadir     = flag.String("datadir", "", "Working directory; will be CREATED. MUST NOT exist for clean-start runs.")
		workload    = flag.String("workload", "random_write", "Workload: random_write | random_read_warm | random_read_cold | range_scan | state_sim | mixed_rw")
		keys        = flag.Uint64("keys", 1_000_000, "Number of keys for the workload")
		valueSize   = flag.Int("value-size", 70, "Bytes per value")
		pageKiB     = flag.Uint64("page-kib", 4, "MDBX page size in KiB (4 / 8 / 16 / 32 / 64)")
		dirtyMB     = flag.Uint64("dirty-mb", 2048, "MDBX dirty pool in MiB")
		commitEvery = flag.Uint64("commit-every", 0, "Commit + reopen the RwTx every N inserts (0 = once at end). state_sim ignores this.")
		csv         = flag.Bool("csv", false, "Emit CSV row to stdout (header on stderr)")
		seed        = flag.Int64("seed", 42, "RNG seed (deterministic key generation)")
	)
	flag.Parse()

	if *backend != "mdbx" {
		fmt.Fprintln(os.Stderr, "ERROR: only --backend=mdbx supported in this version")
		os.Exit(2)
	}
	if *datadir == "" {
		*datadir = filepath.Join(os.TempDir(), fmt.Sprintf("kvb-%d", time.Now().UnixNano()))
	}
	if _, err := os.Stat(filepath.Join(*datadir, "mdbx.dat")); err == nil {
		fmt.Fprintf(os.Stderr, "ERROR: datadir %s already contains mdbx.dat — refusing to clobber. Use a fresh path.\n", *datadir)
		os.Exit(2)
	}
	if err := os.MkdirAll(*datadir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: mkdir %s: %v\n", *datadir, err)
		os.Exit(2)
	}

	if err := run(logger, *backend, *datadir, *workload, *keys, *valueSize, *pageKiB, *dirtyMB, *commitEvery, *csv, *seed); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}

func run(logger log2.Logger, backend, datadir, workload string, keys uint64, valueSize int, pageKiB, dirtyMB, commitEvery uint64, csv bool, seed int64) error {
	if pageKiB&(pageKiB-1) != 0 || pageKiB < 4 || pageKiB > 64 {
		return fmt.Errorf("--page-kib must be a power of two between 4 and 64; got %d", pageKiB)
	}

	// Configure MDBX with the requested page size + a single test
	// table. The TableCfg is global (kv.ChaindataTablesCfg) — our
	// wrapper overrides it just for this benchmark process.
	kv.ChaindataTablesCfg[benchTable] = kv.TableCfgItem{}

	db, err := mdbx.NewMDBX(logger).
		Path(datadir).
		Label(kv.ChainDB).
		PageSize(pageKiB * 1024).
		DirtySpace(uint64(datasize.ByteSize(dirtyMB) * datasize.MB)).
		WriteMap().
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
		Open(nil)
	if err != nil {
		return fmt.Errorf("open MDBX: %w", err)
	}
	defer db.Close()

	res, err := runWorkload(workload, db, keys, valueSize, commitEvery, seed)
	if err != nil {
		return err
	}

	// File size on disk after the workload (NVMe-resident, not
	// in-memory) — the realistic measure of write-amp.
	mdbxBytes, _ := dirSize(datadir)
	res.MDBXFileBytes = mdbxBytes

	emitResult(res, backend, workload, keys, valueSize, pageKiB, dirtyMB, csv)
	return nil
}

// Result holds one workload run's measurements.
type Result struct {
	Op            string        // "write" | "read" | "scan" | "mixed"
	Count         uint64        // operations executed
	Wall          time.Duration // total wall time
	OpsPerSec     float64
	LatNs         []int64 // sampled per-op latencies in ns; computed p50/p99 below
	P50Ns         int64
	P99Ns         int64
	P999Ns        int64
	HeapAllocMB   float64 // peak via runtime.ReadMemStats
	NumGC         uint32
	MDBXFileBytes uint64
}

func emitResult(r *Result, backend, workload string, keys uint64, valueSize int, pageKiB, dirtyMB uint64, csv bool) {
	if csv {
		// Header (once, on stderr so it doesn't pollute stdout CSV body)
		fmt.Fprintln(os.Stderr, "backend,workload,keys,value_size,page_kib,dirty_mb,wall_s,ops_s,p50_us,p99_us,p999_us,heap_mb,num_gc,mdbx_mb")
		fmt.Printf("%s,%s,%d,%d,%d,%d,%.3f,%.0f,%.2f,%.2f,%.2f,%.1f,%d,%.1f\n",
			backend, workload, keys, valueSize, pageKiB, dirtyMB,
			r.Wall.Seconds(),
			r.OpsPerSec,
			float64(r.P50Ns)/1000.0,
			float64(r.P99Ns)/1000.0,
			float64(r.P999Ns)/1000.0,
			r.HeapAllocMB,
			r.NumGC,
			float64(r.MDBXFileBytes)/(1024*1024))
	} else {
		fmt.Printf("backend       : %s\n", backend)
		fmt.Printf("workload      : %s\n", workload)
		fmt.Printf("keys          : %d (value=%d B)\n", keys, valueSize)
		fmt.Printf("page-kib      : %d   dirty-mb: %d\n", pageKiB, dirtyMB)
		fmt.Printf("wall          : %v\n", r.Wall)
		fmt.Printf("ops/s         : %.0f\n", r.OpsPerSec)
		fmt.Printf("latency p50   : %.2f µs\n", float64(r.P50Ns)/1000.0)
		fmt.Printf("latency p99   : %.2f µs\n", float64(r.P99Ns)/1000.0)
		fmt.Printf("latency p99.9 : %.2f µs\n", float64(r.P999Ns)/1000.0)
		fmt.Printf("heap alloc    : %.1f MB\n", r.HeapAllocMB)
		fmt.Printf("num GC        : %d\n", r.NumGC)
		fmt.Printf("MDBX on-disk  : %.1f MB\n", float64(r.MDBXFileBytes)/(1024*1024))
	}
}

// computePercentiles fills P50/P99/P999 from a sample of latencies.
// We don't store every op's latency — for 100M ops the slice would be
// 800 MB and dominate the bench memory. workloads.go samples 1 in N.
func computePercentiles(r *Result) {
	if len(r.LatNs) == 0 {
		return
	}
	sort.Slice(r.LatNs, func(i, j int) bool { return r.LatNs[i] < r.LatNs[j] })
	r.P50Ns = r.LatNs[len(r.LatNs)*50/100]
	r.P99Ns = r.LatNs[len(r.LatNs)*99/100]
	if idx := len(r.LatNs) * 999 / 1000; idx < len(r.LatNs) {
		r.P999Ns = r.LatNs[idx]
	}
}

// snapshotMem fills HeapAllocMB / NumGC from runtime stats.
func snapshotMem(r *Result) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	r.HeapAllocMB = float64(m.HeapAlloc) / (1024 * 1024)
	r.NumGC = m.NumGC
}

// dirSize returns total bytes used by all files under root.
func dirSize(root string) (uint64, error) {
	var total uint64
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += uint64(info.Size())
		}
		return nil
	})
	return total, err
}
