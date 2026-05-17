// coldstore-verify-storage: rebuild the same (addr,slot)→value map
// from storcs that the builder used, then spot-check the coldstore
// reader against it.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/n42blockchain/N42/internal/coldstore"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func emit(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, format, a...)
}

func main() {
	var (
		storcs   = flag.String("storcs", "", "freezer dir with storcs.cidx")
		coldDir  = flag.String("cold", "", "coldstore dir")
		prefix   = flag.String("prefix", "storage", "coldstore prefix")
		startBlk = flag.Uint64("start", 0, "replay start (same as build)")
		blocks   = flag.Uint64("blocks", 5000, "replay blocks (same as build)")
		samples  = flag.Int("samples", 1000, "random keys to spot-check")
		seed     = flag.Int64("seed", 0, "RNG seed (0 = time)")
	)
	flag.Parse()
	if *storcs == "" || *coldDir == "" {
		emit("usage: coldstore-verify-storage --storcs <freezer> --cold <dir> [--start B] [--blocks N]\n")
		os.Exit(1)
	}
	if *seed == 0 {
		*seed = time.Now().UnixNano()
	}
	rng := rand.New(rand.NewSource(*seed))

	fr, err := freezer.NewReadOnly(*storcs)
	must(err, "open freezer")
	defer fr.Close()
	tbl, err := fr.EnsureTableCompressed(freezer.TableStorageChanges, "c")
	must(err, "open storcs")
	end := *startBlk + *blocks
	if end > tbl.Items() {
		end = tbl.Items()
	}

	r, err := coldstore.Open(*coldDir, *prefix)
	must(err, "open coldstore")
	defer r.Close()
	emit("Coldstore opened: %d pages, keyLen=%d\n", r.PageCount(), r.KeyLen())

	emit("Replaying storcs [%d, %d) to rebuild ground truth...\n", *startBlk, end)
	current := make(map[string][]byte, 1<<20)
	t0 := time.Now()
	var applies uint64
	for blk := *startBlk; blk < end; blk++ {
		data, err := tbl.Retrieve(blk)
		if err != nil {
			continue
		}
		changes, err := ethel.DecodeStorageChanges(data)
		if err != nil {
			continue
		}
		for _, c := range changes {
			ck := string(c.CompositeKey)
			if len(c.NewValue) == 0 {
				delete(current, ck)
			} else {
				v := make([]byte, len(c.NewValue))
				copy(v, c.NewValue)
				current[ck] = v
			}
			applies++
		}
	}
	emit("Ground-truth: %d unique keys, %d applies, %s elapsed\n",
		len(current), applies, time.Since(t0).Truncate(time.Second))

	if len(current) == 0 {
		die("ground-truth empty")
	}

	// Index keys for random sampling.
	keys := make([][]byte, 0, len(current))
	for k := range current {
		keys = append(keys, []byte(k))
	}
	if *samples > len(keys) {
		*samples = len(keys)
	}

	// Positive: random sample keys, expect coldstore returns exact match.
	matches, mismatches, missing := 0, 0, 0
	t0 = time.Now()
	for i := 0; i < *samples; i++ {
		k := keys[rng.Intn(len(keys))]
		want := current[string(k)]
		got, ok, err := r.Get(k)
		if err != nil {
			emit("  Get %x err: %v\n", k, err)
			mismatches++
			continue
		}
		if !ok {
			missing++
			if missing < 5 {
				emit("  MISSING key=%x  want=%x\n", k, want)
			}
			continue
		}
		if !bytes.Equal(got, want) {
			mismatches++
			if mismatches < 5 {
				emit("  MISMATCH key=%x  cold=%x  want=%x\n", k, got, want)
			}
			continue
		}
		matches++
	}
	posElapsed := time.Since(t0)

	// Negative: 1000 random keys that are NOT in ground truth.
	negFalse := 0
	negTries := 1000
	t0 = time.Now()
	for i := 0; i < negTries; i++ {
		var k [52]byte
		rng.Read(k[:])
		if _, exists := current[string(k[:])]; exists {
			continue // accidentally a real key, skip
		}
		got, ok, err := r.Get(k[:])
		if err != nil {
			continue
		}
		if ok {
			negFalse++
			if negFalse < 5 {
				emit("  unexpected hit for random key %x = %x\n", k[:], got)
			}
		}
	}
	negElapsed := time.Since(t0)

	emit("\n=== Verify ===\n")
	emit("  positive samples : %d\n", *samples)
	emit("  matches          : %d  (%.2f%%)\n", matches, float64(matches)*100/float64(*samples))
	emit("  missing          : %d\n", missing)
	emit("  mismatches       : %d\n", mismatches)
	emit("  positive latency : %.2f µs/lookup\n", float64(posElapsed.Microseconds())/float64(*samples))
	emit("  negative tries   : %d (random non-existent keys)\n", negTries)
	emit("  negative latency : %.2f µs/lookup\n", float64(negElapsed.Microseconds())/float64(negTries))
	emit("  false positives  : %d\n", negFalse)
	if missing > 0 || mismatches > 0 || negFalse > 0 {
		os.Exit(1)
	}
	emit("\n  ALL %d POSITIVE LOOKUPS MATCHED, %d NEGATIVE LOOKUPS REJECTED ✓\n",
		matches, negTries-negFalse)
}

func must(err error, what string) {
	if err != nil {
		die("%s: %v", what, err)
	}
}

func die(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
