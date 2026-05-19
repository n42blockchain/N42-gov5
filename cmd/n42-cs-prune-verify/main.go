// n42-cs-prune-verify: round-trip check the warm freezer built by
// n42-cs-prune against the source freezer. For random sampled blocks
// in [base, head], compare warm.Retrieve(blk) bytes-for-bytes with
// src.Retrieve(blk).
package main

import (
	"bytes"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/n42blockchain/N42/internal/cs"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	srcDir := flag.String("src", "", "source (original full) freezer dir")
	warmDir := flag.String("warm", "", "warm freezer dir produced by n42-cs-prune")
	samples := flag.Int("samples", 1000, "random samples per table")
	tablesFlag := flag.String("tables", "acctcs,storcs", "comma-separated CS table names")
	seed := flag.Int64("seed", 0, "RNG seed (0=time)")
	flag.Parse()

	if *srcDir == "" || *warmDir == "" {
		fmt.Fprintln(os.Stderr, "usage: n42-cs-prune-verify --src <full> --warm <warm-dir> [--samples N]")
		os.Exit(1)
	}
	if *seed == 0 {
		*seed = time.Now().UnixNano()
	}
	rng := rand.New(rand.NewSource(*seed))

	src, err := freezer.NewReadOnly(*srcDir)
	must(err, "open src")
	defer src.Close()

	warm, err := cs.Open(*warmDir)
	must(err, "open warm")
	defer warm.Close()

	meta := warm.Meta()
	fmt.Printf("Warm meta: base=%d head=%d keep=%d created=%s\n",
		meta.BaseBlock, meta.HeadBlock, meta.KeepBlocks, meta.CreatedAt.Format(time.RFC3339))
	fmt.Printf("Sampling %d blocks per table from [%d, %d]\n\n",
		*samples, meta.BaseBlock, meta.HeadBlock)

	overallMatch, overallMism, overallMiss := 0, 0, 0
	for _, tname := range parseTables(*tablesFlag) {
		fmt.Printf("=== %s ===\n", tname)
		srcTbl, err := src.EnsureTableCompressed(tname, "c")
		must(err, "open src table "+tname)

		matches, miss, mism := 0, 0, 0
		for i := 0; i < *samples; i++ {
			blk := meta.BaseBlock + uint64(rng.Int63n(int64(meta.KeepBlocks)))
			srcData, srcErr := srcTbl.Retrieve(blk)
			warmData, warmErr := warm.Retrieve(tname, blk)
			if srcErr != nil || warmErr != nil {
				if mism < 5 {
					fmt.Fprintf(os.Stderr, "  ERR blk=%d srcErr=%v warmErr=%v\n", blk, srcErr, warmErr)
				}
				mism++
				continue
			}
			if !bytes.Equal(srcData, warmData) {
				if mism < 5 {
					fmt.Fprintf(os.Stderr, "  MISMATCH blk=%d srcLen=%d warmLen=%d\n",
						blk, len(srcData), len(warmData))
				}
				mism++
				continue
			}
			matches++
			_ = miss
		}
		fmt.Printf("  matches=%d  mismatches=%d  missing=%d  (samples=%d)\n",
			matches, mism, miss, *samples)
		overallMatch += matches
		overallMism += mism
		overallMiss += miss

		// Also verify out-of-window queries return ErrOutOfWindow.
		if meta.BaseBlock > 0 {
			oldBlk := meta.BaseBlock - 1
			_, err := warm.Retrieve(tname, oldBlk)
			if err == cs.ErrOutOfWindow {
				fmt.Printf("  ✓ out-of-window blk=%d correctly rejected\n", oldBlk)
			} else {
				fmt.Fprintf(os.Stderr, "  WARN out-of-window check returned err=%v (expected ErrOutOfWindow)\n", err)
			}
		}
		fmt.Println()
	}

	fmt.Printf("=== Verify ===\n")
	fmt.Printf("  total matches    : %d\n", overallMatch)
	fmt.Printf("  total mismatches : %d\n", overallMism)
	fmt.Printf("  total missing    : %d\n", overallMiss)
	if overallMism > 0 || overallMiss > 0 {
		os.Exit(1)
	}
	fmt.Printf("\n  ALL SAMPLES MATCH ✓ warm freezer is byte-identical to src in [base,head]\n")
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

func must(err error, what string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %s: %v\n", what, err)
		os.Exit(1)
	}
}
