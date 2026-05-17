// coldstore-build-storage: build a coldstore .kv + .idx pair from N42
// storcs (storage changesets) over a block range. Accumulates the
// current value of every touched (addr, slot) by replaying writes,
// sorts, then emits the static cold-tier files.
//
// Why storcs instead of PlainState MDBX: on Windows the PlainState
// dup-sort cursor over a 280 GB table explodes the mmap working set
// (88 GB RSS / 14 min with zero rows emitted, observed 2026-05-17).
// storcs is sequential cdat reads + cheap RLP-ish decode, completes
// in seconds per 1000 blocks. Suitable for v1 validation up to the
// per-process memory budget for the in-memory accumulator
// (~50 GB for ~500M unique keys at ~100 B map-entry overhead).
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"sort"
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
		storcs   = flag.String("storcs", "", "freezer dir containing storcs.cidx (e.g. D:\\N42-eth1177\\chain\\freezer)")
		out      = flag.String("out", "", "output dir for .kv + .idx")
		prefix   = flag.String("prefix", "storage", "filename prefix")
		startBlk = flag.Uint64("start", 0, "starting block")
		blocks   = flag.Uint64("blocks", 5000, "blocks to replay")
		pageSize = flag.Int("page", 64, "entries per page")
		progEv   = flag.Uint64("prog", 500, "progress every N blocks")
	)
	flag.Parse()
	if *storcs == "" || *out == "" {
		emit("usage: coldstore-build-storage --storcs <freezer> --out <dir> [--start B] [--blocks N]\n")
		os.Exit(1)
	}
	if err := os.MkdirAll(*out, 0755); err != nil {
		die("mkdir out: %v", err)
	}

	fr, err := freezer.NewReadOnly(*storcs)
	must(err, "open freezer")
	defer fr.Close()
	tbl, err := fr.EnsureTableCompressed(freezer.TableStorageChanges, "c")
	must(err, "open storcs table")
	maxItems := tbl.Items()
	emit("storcs.cidx items: %d\n", maxItems)

	end := *startBlk + *blocks
	if end > maxItems {
		end = maxItems
	}
	emit("Replaying storcs [%d, %d) → %s/%s.{kv,idx}\n", *startBlk, end, *out, *prefix)

	// In-memory accumulator: latest value per (addr, slot).
	current := make(map[string][]byte, 1<<20)

	var totalApplies uint64
	t0 := time.Now()
	for blk := *startBlk; blk < end; blk++ {
		data, err := tbl.Retrieve(blk)
		if err != nil {
			emit("  retrieve block %d err: %v\n", blk, err)
			continue
		}
		changes, err := ethel.DecodeStorageChanges(data)
		if err != nil {
			emit("  decode block %d err: %v\n", blk, err)
			continue
		}
		for _, c := range changes {
			ck := string(c.CompositeKey) // 52 B
			if len(c.NewValue) == 0 {
				delete(current, ck) // slot reset to 0 → pruned
			} else {
				v := make([]byte, len(c.NewValue))
				copy(v, c.NewValue)
				current[ck] = v
			}
			totalApplies++
		}
		if (blk-*startBlk+1)%*progEv == 0 {
			elapsed := time.Since(t0).Seconds()
			emit("  block %d  done=%d/%d  applies=%d  uniq=%d  (%.0f blk/s)\n",
				blk, blk-*startBlk+1, end-*startBlk, totalApplies, len(current),
				float64(blk-*startBlk+1)/elapsed)
		}
	}
	emit("\nReplay done: %d applies, %d unique keys retained, %s elapsed\n",
		totalApplies, len(current), time.Since(t0).Truncate(time.Second))

	// Sort keys.
	emit("Sorting %d keys...\n", len(current))
	tSort := time.Now()
	keys := make([][]byte, 0, len(current))
	for k := range current {
		keys = append(keys, []byte(k))
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 })
	emit("Sort done in %s\n", time.Since(tSort).Truncate(time.Millisecond))

	// Write coldstore.
	w, err := coldstore.NewWriter(*out, *prefix, 52, *pageSize)
	must(err, "open writer")

	emit("Writing coldstore...\n")
	tWrite := time.Now()
	for _, k := range keys {
		if err := w.Append(k, current[string(k)]); err != nil {
			die("Append %x: %v", k, err)
		}
	}
	must(w.Close(), "close writer")
	emit("Write done in %s\n", time.Since(tWrite).Truncate(time.Millisecond))

	s := w.Stats()
	idxSize := uint64(coldstore.HeaderSize) + s.PageCount*(52+8)
	total := s.TotalKvSize + idxSize
	emit("\n=== Coldstore stats ===\n")
	emit("  keys       : %d\n", s.KeyCount)
	emit("  pages      : %d  (entries/page = %d)\n", s.PageCount, *pageSize)
	emit("  kv size    : %s   (%.2f B/key)\n",
		humanBytes(s.TotalKvSize), float64(s.TotalKvSize)/float64(s.KeyCount))
	emit("  idx size   : %s   (%.2f B/key, %d B/page entry)\n",
		humanBytes(idxSize), float64(idxSize)/float64(s.KeyCount), 52+8)
	emit("  TOTAL      : %s   (%.2f B/key)\n",
		humanBytes(total), float64(total)/float64(s.KeyCount))
	rawSize := s.KeyCount * uint64(52+32)
	emit("  vs raw 84B : %.2fx compression (%s saved)\n",
		float64(rawSize)/float64(total), humanBytes(rawSize-total))
	emit("  vs Erigon  : ~%.0f%%  (using 60 B/key baseline incl. .bt + .efi)\n",
		float64(total)*100/float64(s.KeyCount*60))
}

func humanBytes(b uint64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.2f GB", float64(b)/GB)
	case b >= MB:
		return fmt.Sprintf("%.2f MB", float64(b)/MB)
	case b >= KB:
		return fmt.Sprintf("%.2f KB", float64(b)/KB)
	}
	return fmt.Sprintf("%d B", b)
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
