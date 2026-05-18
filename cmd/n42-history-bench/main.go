// n42-history-bench: measure access performance of a history coldstore.
//
// Reports:
//   - random Get: µs/lookup (single thread)
//   - sequential Get (locality friendly): µs/lookup
//   - concurrent Get: queries/sec at varying parallelism
//   - cold-cache vs warm-cache (if --drop-cache)
package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/history"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func emit(format string, a ...interface{}) { fmt.Fprintf(os.Stderr, format, a...) }
func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}

type getFn func([]byte) ([]byte, bool, error)

func main() {
	storeDir := flag.String("store", "", "history coldstore dir")
	prefix := flag.String("prefix", "storage", "domain prefix")
	mphf := flag.Bool("mphf", false, "open as MPHF+fp store")
	freezerDir := flag.String("freezer", "", "(optional) freezer dir to source real keys")
	startBlk := flag.Uint64("start", 0, "block range start (for sourcing keys, must match build)")
	endBlk := flag.Uint64("end", 0, "block range end")
	samples := flag.Int("samples", 50000, "samples per workload")
	concurrent := flag.String("concurrent", "1,2,4,8,16,32", "comma-separated worker counts")
	seed := flag.Int64("seed", 0, "RNG seed (0=time)")
	flag.Parse()

	if *storeDir == "" || *freezerDir == "" {
		emit("usage: n42-history-bench --store <dir> --prefix DOM [--mphf] --freezer <dir> --start B --end B [--samples N]\n")
		os.Exit(1)
	}
	if *seed == 0 {
		*seed = time.Now().UnixNano()
	}
	rng := rand.New(rand.NewSource(*seed))

	// Source keys from freezer (so we benchmark realistic keys, not random).
	emit("Sourcing keys from freezer [%d, %d)...\n", *startBlk, *endBlk)
	keys := sourceKeys(*freezerDir, *prefix, *startBlk, *endBlk, *mphf)
	if len(keys) == 0 {
		fatal("no keys sourced")
	}
	emit("Sourced %d unique keys.\n", len(keys))

	// Open reader.
	var get getFn
	var mode string
	if *mphf {
		r, err := history.OpenMPHF(*storeDir, *prefix)
		if err != nil {
			fatal("OpenMPHF: %v", err)
		}
		defer r.Close()
		get = r.Get
		mode = fmt.Sprintf("MPHF (%d pages, %d keys)", r.PageCount(), r.KeyCount())
	} else {
		r, err := history.Open(*storeDir, *prefix)
		if err != nil {
			fatal("Open: %v", err)
		}
		defer r.Close()
		get = r.Get
		mode = fmt.Sprintf("plain (%d pages, keyLen=%d)", r.PageCount(), r.KeyLen())
	}
	emit("Reader: %s\n\n", mode)

	// Pre-pick sample indices.
	sampleIdx := make([]int, *samples)
	for i := range sampleIdx {
		sampleIdx[i] = rng.Intn(len(keys))
	}

	// --- Warmup ---
	emit("Warmup: 1000 lookups...\n")
	for i := 0; i < 1000; i++ {
		k := keys[sampleIdx[i%*samples]]
		// MPHF version returns blob; for storage-grouped we want addr only.
		if *prefix == "storage-grouped" {
			get(k[:20])
		} else {
			get(k)
		}
		if i == 0 {
			if err := checkOne(get, k, *prefix); err != nil {
				emit("WARN warmup lookup mismatch: %v\n", err)
			}
		}
	}

	// --- Workload 1: single-thread random ---
	emit("=== Single-thread random ===\n")
	t0 := time.Now()
	var matched, miss int64
	for _, i := range sampleIdx {
		k := keys[i]
		var ok bool
		if *prefix == "storage-grouped" {
			_, ok, _ = get(k[:20])
		} else {
			_, ok, _ = get(k)
		}
		if ok {
			matched++
		} else {
			miss++
		}
	}
	elapsed := time.Since(t0)
	usPerLookup := float64(elapsed.Microseconds()) / float64(*samples)
	qps := float64(*samples) / elapsed.Seconds()
	emit("  %d samples in %s\n", *samples, elapsed.Truncate(time.Millisecond))
	emit("  matched=%d miss=%d\n", matched, miss)
	emit("  %.2f µs/lookup, %.0f qps\n\n", usPerLookup, qps)

	// --- Workload 2: sequential by key (locality-friendly) ---
	emit("=== Sequential by sorted key ===\n")
	sortedKeys := make([][]byte, *samples)
	for i, idx := range sampleIdx {
		sortedKeys[i] = keys[idx]
	}
	sort.Slice(sortedKeys, func(i, j int) bool {
		for k := 0; k < len(sortedKeys[i]) && k < len(sortedKeys[j]); k++ {
			if sortedKeys[i][k] != sortedKeys[j][k] {
				return sortedKeys[i][k] < sortedKeys[j][k]
			}
		}
		return false
	})
	t0 = time.Now()
	matched, miss = 0, 0
	for _, k := range sortedKeys {
		var ok bool
		if *prefix == "storage-grouped" {
			_, ok, _ = get(k[:20])
		} else {
			_, ok, _ = get(k)
		}
		if ok {
			matched++
		} else {
			miss++
		}
	}
	elapsed = time.Since(t0)
	usPerLookup = float64(elapsed.Microseconds()) / float64(*samples)
	qps = float64(*samples) / elapsed.Seconds()
	emit("  %d samples in %s (sorted-key locality)\n", *samples, elapsed.Truncate(time.Millisecond))
	emit("  %.2f µs/lookup, %.0f qps\n\n", usPerLookup, qps)

	// --- Workload 3: concurrent random ---
	emit("=== Concurrent random (varying workers) ===\n")
	workers := parseConcurrent(*concurrent)
	emit("  %-6s | %-10s | %-12s | %-10s\n", "wkrs", "qps", "µs/lookup", "speedup")
	emit("  -------|------------|--------------|-----------\n")
	var baseQPS float64
	for _, w := range workers {
		qps := concurrentBench(get, keys, sampleIdx, w, *prefix)
		if baseQPS == 0 {
			baseQPS = qps
		}
		emit("  %-6d | %-10.0f | %-12.2f | %.2fx\n", w, qps, 1e6/qps, qps/baseQPS)
	}

	emit("\nCPUs: %d\n", runtime.NumCPU())
}

func sourceKeys(freezerDir, prefix string, startBlk, endBlk uint64, mphf bool) [][]byte {
	fr, err := freezer.NewReadOnly(freezerDir)
	if err != nil {
		fatal("open freezer: %v", err)
	}
	defer fr.Close()
	var tableName string
	switch prefix {
	case "account":
		tableName = freezer.TableAccountChanges
	case "storage", "storage-grouped":
		tableName = freezer.TableStorageChanges
	default:
		fatal("unknown prefix: %s", prefix)
	}
	tbl, err := fr.EnsureTableCompressed(tableName, "c")
	if err != nil {
		fatal("open table: %v", err)
	}
	if endBlk == 0 || endBlk > tbl.Items() {
		endBlk = tbl.Items()
	}
	seen := make(map[string]struct{}, 1<<20)
	for blk := startBlk; blk < endBlk; blk++ {
		data, err := tbl.Retrieve(blk)
		if err != nil || len(data) == 0 {
			continue
		}
		switch prefix {
		case "account":
			cs, err := ethel.DecodeAccountChanges(data)
			if err != nil {
				continue
			}
			for _, c := range cs {
				seen[string(c.Address[:])] = struct{}{}
			}
		case "storage", "storage-grouped":
			cs, err := ethel.DecodeStorageChanges(data)
			if err != nil {
				continue
			}
			for _, c := range cs {
				seen[string(c.CompositeKey)] = struct{}{}
			}
		}
	}
	out := make([][]byte, 0, len(seen))
	for k := range seen {
		out = append(out, []byte(k))
	}
	return out
}

func concurrentBench(get getFn, keys [][]byte, sampleIdx []int, workers int, prefix string) float64 {
	chunkSize := len(sampleIdx) / workers
	var wg sync.WaitGroup
	t0 := time.Now()
	var totalDone int64
	for w := 0; w < workers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if w == workers-1 {
			end = len(sampleIdx)
		}
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			var localDone int64
			for i := start; i < end; i++ {
				k := keys[sampleIdx[i]]
				if prefix == "storage-grouped" {
					get(k[:20])
				} else {
					get(k)
				}
				localDone++
			}
			atomic.AddInt64(&totalDone, localDone)
		}(start, end)
	}
	wg.Wait()
	elapsed := time.Since(t0)
	return float64(totalDone) / elapsed.Seconds()
}

func parseConcurrent(s string) []int {
	out := []int{}
	cur := 0
	for i, c := range s {
		if c == ',' || i == len(s)-1 {
			if i == len(s)-1 && c != ',' {
				cur = cur*10 + int(c-'0')
			}
			if cur > 0 {
				out = append(out, cur)
			}
			cur = 0
		} else if c >= '0' && c <= '9' {
			cur = cur*10 + int(c-'0')
		}
	}
	return out
}

func checkOne(get getFn, k []byte, prefix string) error {
	if prefix == "storage-grouped" {
		k = k[:20]
	}
	blob, ok, err := get(k)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("expected key not found: %x", k)
	}
	_ = blob
	return nil
}
