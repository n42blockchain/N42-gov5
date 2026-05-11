// n42-cs-cmp compares two n42 freezer dirs block-by-block on the given
// table ("acctcs", "storcs", or "witness") and reports the first block
// whose payload bytes differ.
//
// Use case: a freezer archive recorded at known-good code (e.g.
// D:\N42-eth1456-verified\1456verified, recorded during the
// 2026-04-28..05-03 verified period) vs the current main archive
// (D:\N42-eth1177\chain\freezer). If the verified-period range is
// byte-identical between the two, the divergence is strictly in the
// post-checkpoint section and we can stop suspecting early-range
// regressions.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func openTbl(dir, name string) (*freezer.FreezerTable, error) {
	tbl, err := freezer.NewFreezerTableCompressedReadOnly(dir, name, "c")
	if err != nil {
		return nil, err
	}
	tbl.ForceBatchSize(freezer.BatchSize)
	return tbl, nil
}

func main() {
	aDir := flag.String("a", "", "freezer dir A (REQUIRED)")
	bDir := flag.String("b", "", "freezer dir B (REQUIRED)")
	table := flag.String("table", "acctcs", "table to compare: acctcs / storcs / witness")
	from := flag.Uint64("from", 0, "start block (inclusive)")
	to := flag.Uint64("to", 0, "end block (inclusive); 0 = min(a.Items, b.Items)-1")
	maxDiffs := flag.Int("max-diffs", 5, "stop after printing this many diffs")
	progressEvery := flag.Uint64("progress", 100000, "log progress every N blocks (0 = silent)")
	flag.Parse()
	if *aDir == "" || *bDir == "" {
		fmt.Fprintln(os.Stderr, "ERROR: --a and --b are required")
		os.Exit(2)
	}

	aT, err := openTbl(*aDir, *table)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open A:", err)
		os.Exit(1)
	}
	defer aT.Close()
	bT, err := openTbl(*bDir, *table)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open B:", err)
		os.Exit(1)
	}
	defer bT.Close()

	end := *to
	if end == 0 {
		end = aT.Items() - 1
		if bT.Items()-1 < end {
			end = bT.Items() - 1
		}
	}
	fmt.Printf("comparing %s in blocks [%d, %d]\nA=%s\nB=%s\n", *table, *from, end, *aDir, *bDir)

	t0 := time.Now()
	diffs := 0
	for b := *from; b <= end; b++ {
		va, err := aT.Retrieve(b)
		if err != nil {
			fmt.Printf("A retrieve %d err: %v\n", b, err)
			break
		}
		vb, err := bT.Retrieve(b)
		if err != nil {
			fmt.Printf("B retrieve %d err: %v\n", b, err)
			break
		}
		if !bytes.Equal(va, vb) {
			fmt.Printf("\nblock=%d DIFF a=%dB b=%dB\n", b, len(va), len(vb))
			if len(va) <= 256 && len(vb) <= 256 {
				fmt.Printf("  a=%x\n  b=%x\n", va, vb)
			} else {
				// print common prefix length
				n := len(va)
				if len(vb) < n {
					n = len(vb)
				}
				diffAt := -1
				for i := 0; i < n; i++ {
					if va[i] != vb[i] {
						diffAt = i
						break
					}
				}
				fmt.Printf("  common-prefix=%d total_a=%d total_b=%d firstDiffOffset=%d\n",
					n, len(va), len(vb), diffAt)
				lo := diffAt - 8
				if lo < 0 {
					lo = 0
				}
				hi := diffAt + 32
				if hi > len(va) {
					hi = len(va)
				}
				hi2 := diffAt + 32
				if hi2 > len(vb) {
					hi2 = len(vb)
				}
				fmt.Printf("  a@%d..%d: %x\n", lo, hi, va[lo:hi])
				fmt.Printf("  b@%d..%d: %x\n", lo, hi2, vb[lo:hi2])
			}
			diffs++
			if diffs >= *maxDiffs {
				fmt.Printf("\nstopping after %d diffs\n", diffs)
				break
			}
		}
		if *progressEvery > 0 && b%*progressEvery == 0 && b > 0 {
			rate := float64(b-*from+1) / time.Since(t0).Seconds()
			fmt.Fprintf(os.Stderr, "  scanned to %d (%.0f blk/s, diffs=%d, elapsed=%v)\n",
				b, rate, diffs, time.Since(t0).Truncate(time.Second))
		}
	}
	fmt.Printf("\n=== done === blocks scanned=%d diffs=%d elapsed=%v\n",
		(end-*from+1), diffs, time.Since(t0).Truncate(time.Millisecond))
}
