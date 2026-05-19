// n42-cs-prune: build a "warm" CS freezer containing only the last
// N blocks of acctcs + storcs. Original (full) freezer is untouched;
// user atomically swaps (--swap) or runs once and swaps manually.
//
// Use case: after cmd/n42-history-build produces a full archive
// history, the original acctcs/storcs freezer entries for blocks
// covered by history are largely redundant — they serve only unwind
// for the last few PoS finality windows. Keeping last 7 days
// (50,400 blocks) is >> 100x PoS finality (32 slots) and saves
// hundreds of GB.
//
// Modes:
//
//	one-shot:   n42-cs-prune --src ... --dst ...
//	atomic:     n42-cs-prune --src ... --dst ... --swap
//	scheduled:  n42-cs-prune --src ... --dst ... --swap --loop 168h
//
// Atomic swap writes to <dst>.staging/ first, verifies sizes, then
// renames:  <dst> → <dst>.old, <dst>.staging → <dst>. The old dir
// can be reclaimed after a grace period (default kept; user removes).
//
// Loop mode prunes on the configured interval forever (intended for
// systemd / supervisord / kubernetes operator wrap). The first cycle
// fires immediately, then waits --loop duration between cycles.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	swap := flag.Bool("swap", false, "atomic mode: write to <dst>.staging then rename to <dst> on success (keeps <dst>.old as backup)")
	loop := flag.Duration("loop", 0, "if non-zero, prune-and-sleep loop forever at this interval (e.g., 168h for weekly). Use with --swap for safe live ops.")
	keepOld := flag.Bool("keep-old", false, "with --swap, keep <dst>.old after swap (default: remove on next successful swap)")
	flag.Parse()

	if *srcDir == "" || *dstDir == "" {
		fmt.Fprintln(os.Stderr, "usage: n42-cs-prune --src <freezer-dir> --dst <warm-dir> [--keep-blocks N] [--swap] [--loop 168h] [--dry-run]")
		os.Exit(1)
	}
	if *srcDir == *dstDir {
		fatal("src and dst must differ")
	}
	if *loop > 0 && !*swap {
		fatal("--loop requires --swap (live rebuild needs atomic dir replacement)")
	}
	if *loop > 0 && *dryRun {
		fatal("--loop is meaningless with --dry-run (no actual writes happen)")
	}

	tables := parseTables(*tablesFlag)

	runOnce := func() error {
		return runOnePrune(*srcDir, *dstDir, *keepBlocks, tables, *dryRun, *swap, *keepOld)
	}

	if *loop == 0 {
		if err := runOnce(); err != nil {
			fatal("prune: %v", err)
		}
		return
	}

	// Loop mode: prune, sleep, repeat. Don't exit on prune errors —
	// log and try again next cycle. Caller's process supervisor
	// (systemd/k8s) handles non-recoverable failures.
	cycle := 0
	for {
		cycle++
		t0 := time.Now()
		fmt.Printf("\n=========================================\n")
		fmt.Printf("=== prune cycle %d @ %s\n", cycle, t0.Format(time.RFC3339))
		fmt.Printf("=========================================\n")
		if err := runOnce(); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR in cycle %d: %v\n", cycle, err)
		} else {
			fmt.Printf("\n=== cycle %d complete in %s; next cycle at %s\n",
				cycle, time.Since(t0).Truncate(time.Second),
				time.Now().Add(*loop).Format(time.RFC3339))
		}
		time.Sleep(*loop)
	}
}

// runOnePrune executes a single prune cycle. With swap=true it writes
// to <dst>.staging then atomically renames into place.
func runOnePrune(srcDir, dstDir string, keepBlocks uint64, tables []string, dryRun, swap, keepOld bool) error {
	srcFr, err := freezer.NewReadOnly(srcDir)
	if err != nil {
		return fmt.Errorf("open src: %w", err)
	}
	defer srcFr.Close()

	writeDir := dstDir
	stagingDir := ""
	oldDir := ""
	if swap {
		stagingDir = dstDir + ".staging"
		oldDir = dstDir + ".old"
		writeDir = stagingDir
		// Clean any leftover from a previous interrupted run.
		if err := os.RemoveAll(stagingDir); err != nil {
			return fmt.Errorf("clean leftover staging: %w", err)
		}
	}

	if !dryRun {
		if err := os.MkdirAll(writeDir, 0755); err != nil {
			return fmt.Errorf("mkdir writeDir: %w", err)
		}
	}

	var dstFr *freezer.Freezer
	if !dryRun {
		dstFr, err = freezer.New(writeDir, 0)
		if err != nil {
			return fmt.Errorf("open dst: %w", err)
		}
		defer dstFr.Close()
	}

	overallSrc, overallDst := uint64(0), uint64(0)
	var tail, head uint64

	for _, tname := range tables {
		fmt.Printf("\n=== %s ===\n", tname)
		tbl, err := srcFr.EnsureTableCompressed(tname, "c")
		if err != nil {
			return fmt.Errorf("open src table %s: %w", tname, err)
		}
		head = tbl.Items()
		if head == 0 {
			fmt.Printf("  empty, skipping\n")
			continue
		}
		if head < keepBlocks {
			tail = 0
			fmt.Printf("  head=%d < keep=%d → keep all, no prune\n", head, keepBlocks)
		} else {
			tail = head - keepBlocks
			fmt.Printf("  head=%d, keep last %d → tail=%d\n", head, keepBlocks, tail)
		}

		srcSize := tableSizeBytes(srcDir, tname)
		fmt.Printf("  src on-disk: %s\n", humanBytes(srcSize))
		overallSrc += srcSize

		if dryRun {
			estimate := srcSize * keepBlocks / head
			if head < keepBlocks {
				estimate = srcSize
			}
			fmt.Printf("  [dry-run] dst estimate: %s (%.2f%% of src)\n",
				humanBytes(estimate), float64(estimate)*100/float64(srcSize))
			continue
		}

		dstTbl, err := dstFr.EnsureTableCompressed(tname, "c")
		if err != nil {
			return fmt.Errorf("open dst table %s: %w", tname, err)
		}

		t0 := time.Now()
		var copied uint64
		for blk := tail; blk < head; blk++ {
			data, err := tbl.Retrieve(blk)
			if err != nil {
				return fmt.Errorf("retrieve src %s[%d]: %w", tname, blk, err)
			}
			dstItem := blk - tail
			if err := dstTbl.Append(dstItem, data); err != nil {
				return fmt.Errorf("append dst %s[item %d, blk %d]: %w", tname, dstItem, blk, err)
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

		dstSize := tableSizeBytes(writeDir, tname)
		ratio := float64(dstSize) * 100 / float64(srcSize)
		fmt.Printf("  dst on-disk: %s (%.2f%% of src)\n", humanBytes(dstSize), ratio)
		overallDst += dstSize
	}

	// Write sidecar meta describing what was kept.
	if !dryRun && head > 0 {
		meta := cs.Meta{
			BaseBlock:      tail,
			HeadBlock:      head - 1,
			KeepBlocks:     head - tail,
			CreatedAt:      time.Now().UTC(),
			SrcFreezerPath: absOrSelf(srcDir),
		}
		if err := cs.WriteMeta(writeDir, meta); err != nil {
			return fmt.Errorf("write meta: %w", err)
		}
		fmt.Printf("\n=== Wrote %s/meta.json: base=%d head=%d (%d blocks) ===\n",
			writeDir, meta.BaseBlock, meta.HeadBlock, meta.KeepBlocks)
	}

	if !dryRun {
		fmt.Printf("\n=== Overall ===\n")
		fmt.Printf("  src total: %s\n", humanBytes(overallSrc))
		fmt.Printf("  dst total: %s\n", humanBytes(overallDst))
		if overallSrc > 0 {
			fmt.Printf("  savings:   %s (%.1f%% reduction)\n",
				humanBytes(overallSrc-overallDst),
				100.0-float64(overallDst)*100/float64(overallSrc))
		}
	}

	// Close dst freezer BEFORE renaming on Windows (files locked while open).
	if dstFr != nil {
		dstFr.Close()
	}

	if !dryRun && swap {
		if err := atomicSwap(stagingDir, dstDir, oldDir, keepOld); err != nil {
			return fmt.Errorf("atomic swap: %w", err)
		}
		fmt.Printf("\n=== Swap complete ===\n")
		fmt.Printf("  active:    %s\n", dstDir)
		if keepOld {
			fmt.Printf("  backup:    %s (--keep-old set; remove when comfortable)\n", oldDir)
		}
		fmt.Printf("\nNote: live n42 nodes need restart (or admin reload endpoint) to pick up new warm tier.\n")
	} else if !dryRun {
		fmt.Printf("\nTo apply (after testing %s works):\n", dstDir)
		fmt.Printf("  mv %s %s.cold-backup\n", srcDir, srcDir)
		fmt.Printf("  mv %s %s\n", dstDir, srcDir)
		fmt.Printf("  rm -rf %s.cold-backup    # only after history+warm verified\n", srcDir)
	}
	return nil
}

// atomicSwap performs:
//   1. rm -rf <old>      (previous backup, if any)
//   2. mv <dst> <old>    (only if dst exists)
//   3. mv <staging> <dst>
//   4. rm -rf <old>      (unless keepOld)
//
// The sequence handles Windows file-lock realities: each rename is
// atomic at the filesystem level; the only window of inconsistency
// is between steps 2 and 3, during which <dst> doesn't exist. A node
// trying to open <dst> in that window sees ENOENT and should retry.
func atomicSwap(staging, dst, old string, keepOld bool) error {
	// Step 1: clean leftover .old from previous run.
	if err := os.RemoveAll(old); err != nil {
		return fmt.Errorf("rm prev .old: %w", err)
	}

	// Step 2: dst → old (only if dst exists).
	if _, err := os.Stat(dst); err == nil {
		if err := os.Rename(dst, old); err != nil {
			return fmt.Errorf("dst → .old rename: %w", err)
		}
	}

	// Step 3: staging → dst.
	if err := os.Rename(staging, dst); err != nil {
		// Roll back step 2 if possible.
		if _, statErr := os.Stat(old); statErr == nil {
			_ = os.Rename(old, dst) // best-effort restore
		}
		return fmt.Errorf("staging → dst rename: %w", err)
	}

	// Step 4: remove backup unless keep-old requested.
	if !keepOld {
		if err := os.RemoveAll(old); err != nil {
			return fmt.Errorf("cleanup .old: %w", err)
		}
	}
	return nil
}

func tableSizeBytes(dir, table string) uint64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total uint64
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), table) {
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
	out := make([]string, 0, 2)
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
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
