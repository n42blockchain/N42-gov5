// n42-cs-prune: build a "warm" CS freezer containing only the last
// N blocks of acctcs + storcs. Original (full) freezer is untouched;
// user atomically swaps if happy with the result.
//
// Use case: after cmd/n42-history-build produces a full archive
// history, the original acctcs/storcs freezer entries for blocks
// covered by history are largely redundant — they serve only unwind
// for the last few PoS finality windows. Keeping last 7 days
// (50,400 blocks) is >> 100× PoS finality (32 slots) and saves
// hundreds of GB.
//
// Pipeline:
//
//	1. Open src freezer (read-only).
//	2. For each of acctcs, storcs:
//	   - head = src.Table(name).Items()
//	   - tail = max(0, head - keep)
//	   - Open dst freezer (writable).
//	   - For blk in [tail, head): copy CS bytes blk → item (blk-tail).
//	3. Write meta.json {BaseBlock=tail, HeadBlock=head-1, ...} to dst.
//	4. Report size before / after.
//
// To apply:
//
//	mv chain/freezer  chain/freezer-cold-backup
//	mv chain/freezer-warm  chain/freezer
//	# old CS data is in freezer-cold-backup; delete after verifying
//	# warm freezer works in tests.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/n42blockchain/N42/internal/cs"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	srcDir := flag.String("src", "", "source freezer dir (e.g., D:/N42-eth1177/chain/freezer)")
	dstDir := flag.String("dst", "", "destination warm freezer dir (will be created)")
	keepBlocks := flag.Uint64("keep-blocks", 50400, "blocks to retain (default 50400 = 7 days × 7200/day)")
	tablesFlag := flag.String("tables", "acctcs,storcs", "comma-separated CS table names to prune")
	dryRun := flag.Bool("dry-run", false, "report sizes but don't write dst")
	flag.Parse()

	if *srcDir == "" || *dstDir == "" {
		fmt.Fprintln(os.Stderr, "usage: n42-cs-prune --src <freezer-dir> --dst <warm-dir> [--keep-blocks N] [--dry-run]")
		os.Exit(1)
	}
	if *srcDir == *dstDir {
		fatal("src and dst must differ")
	}

	tables := parseTables(*tablesFlag)

	srcFr, err := freezer.NewReadOnly(*srcDir)
	must(err, "open src")
	defer srcFr.Close()

	if !*dryRun {
		must(os.MkdirAll(*dstDir, 0755), "mkdir dst")
	}

	var dstFr *freezer.Freezer
	if !*dryRun {
		dstFr, err = freezer.New(*dstDir, 0)
		must(err, "open dst")
		defer dstFr.Close()
	}

	overallSrc, overallDst := uint64(0), uint64(0)
	var tail, head uint64

	for _, tname := range tables {
		fmt.Printf("\n=== %s ===\n", tname)
		tbl, err := srcFr.EnsureTableCompressed(tname, "c")
		must(err, "open src table "+tname)
		head = tbl.Items()
		if head == 0 {
			fmt.Printf("  empty, skipping\n")
			continue
		}
		if head < *keepBlocks {
			tail = 0
			fmt.Printf("  head=%d < keep=%d → keep all, no prune\n", head, *keepBlocks)
		} else {
			tail = head - *keepBlocks
			fmt.Printf("  head=%d, keep last %d → tail=%d\n", head, *keepBlocks, tail)
		}

		srcSize := tableSizeBytes(*srcDir, tname)
		fmt.Printf("  src on-disk: %s\n", humanBytes(srcSize))
		overallSrc += srcSize

		if *dryRun {
			estimate := srcSize * (*keepBlocks) / head
			if head < *keepBlocks {
				estimate = srcSize
			}
			fmt.Printf("  [dry-run] dst estimate: %s (%.2f%% of src)\n",
				humanBytes(estimate), float64(estimate)*100/float64(srcSize))
			continue
		}

		dstTbl, err := dstFr.EnsureTableCompressed(tname, "c")
		must(err, "open dst table "+tname)

		t0 := time.Now()
		var copied uint64
		for blk := tail; blk < head; blk++ {
			data, err := tbl.Retrieve(blk)
			if err != nil {
				fatal("retrieve src %s[%d]: %v", tname, blk, err)
			}
			dstItem := blk - tail
			if err := dstTbl.Append(dstItem, data); err != nil {
				fatal("append dst %s[item %d, blk %d]: %v", tname, dstItem, blk, err)
			}
			copied++
			if copied%10_000 == 0 {
				elapsed := time.Since(t0).Seconds()
				rate := float64(copied) / elapsed
				fmt.Fprintf(os.Stderr, "    copied %d/%d (%.0f blk/s)\n", copied, head-tail, rate)
			}
		}
		dstFr.Sync()
		fmt.Printf("  copied %d blocks in %s\n", copied, time.Since(t0).Truncate(time.Millisecond))

		dstSize := tableSizeBytes(*dstDir, tname)
		ratio := float64(dstSize) * 100 / float64(srcSize)
		fmt.Printf("  dst on-disk: %s (%.2f%% of src)\n", humanBytes(dstSize), ratio)
		overallDst += dstSize
	}

	// Write sidecar meta describing what was kept.
	if !*dryRun && head > 0 {
		meta := cs.Meta{
			BaseBlock:      tail,
			HeadBlock:      head - 1,
			KeepBlocks:     head - tail,
			CreatedAt:      time.Now().UTC(),
			SrcFreezerPath: absOrSelf(*srcDir),
		}
		must(cs.WriteMeta(*dstDir, meta), "write meta")
		fmt.Printf("\n=== Wrote %s/meta.json: base=%d head=%d (%d blocks) ===\n",
			*dstDir, meta.BaseBlock, meta.HeadBlock, meta.KeepBlocks)
	}

	if !*dryRun {
		fmt.Printf("\n=== Overall ===\n")
		fmt.Printf("  src total: %s\n", humanBytes(overallSrc))
		fmt.Printf("  dst total: %s\n", humanBytes(overallDst))
		if overallSrc > 0 {
			fmt.Printf("  savings:   %s (%.1f%% reduction)\n",
				humanBytes(overallSrc-overallDst),
				100.0-float64(overallDst)*100/float64(overallSrc))
		}
		fmt.Printf("\nTo apply (after testing %s works):\n", *dstDir)
		fmt.Printf("  mv %s %s.cold-backup\n", *srcDir, *srcDir)
		fmt.Printf("  mv %s %s\n", *dstDir, *srcDir)
		fmt.Printf("  rm -rf %s.cold-backup    # only after history+warm verified\n", *srcDir)
	}
}

func tableSizeBytes(dir, table string) uint64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total uint64
	for _, e := range entries {
		name := e.Name()
		if len(name) < len(table) || name[:len(table)] != table {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += uint64(info.Size())
	}
	return total
}

func parseTables(s string) []string {
	out := []string{}
	cur := ""
	for _, c := range s {
		if c == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
		} else {
			cur += string(c)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func absOrSelf(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
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
		fatal("%s: %v", what, err)
	}
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
