// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

// check-leaves opens a leaves freezer table in read-only mode and reports
// the cidx-declared item count, spot-checks suspect items, and (with
// --scan-lo/--scan-hi) linearly probes every batch in a range to find
// corrupted holes. The leaves table can have OK→FAIL→OK gaps from
// alignOnResume crashes, so a binary search would miss interior holes;
// the range scan is the only reliable detector.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// scanRange linearly probes one item per batch in [lo, hi] and prints
// contiguous OK / ERR runs. Each Retrieve costs one zstd decode, so the
// stride is one item per 64-item batch.
func scanRange(tbl *freezer.FreezerTable, lo, hi uint64) {
	bs := uint64(freezer.BatchSize)
	loBatch := lo / bs
	hiBatch := hi / bs
	fmt.Printf("\nlinear batch scan [%d, %d] (%d batches):\n", lo, hi, hiBatch-loBatch+1)
	const (
		stateUnknown = iota
		stateOK
		stateErr
	)
	prev := stateUnknown
	streakStart := loBatch
	holes := 0
	tag := func(s int) string {
		if s == stateErr {
			return "ERR"
		}
		return "OK"
	}
	for b := loBatch; b <= hiBatch; b++ {
		idx := b * bs
		_, err := tbl.Retrieve(idx)
		cur := stateOK
		if err != nil {
			cur = stateErr
		}
		if cur != prev {
			if prev != stateUnknown {
				fmt.Printf("  batches [%d..%d] (items %d..%d): %s\n",
					streakStart, b-1, streakStart*bs, b*bs-1, tag(prev))
				if prev == stateErr {
					holes++
				}
			}
			prev = cur
			streakStart = b
		}
	}
	fmt.Printf("  batches [%d..%d] (items %d..%d): %s\n",
		streakStart, hiBatch, streakStart*bs, (hiBatch+1)*bs-1, tag(prev))
	if prev == stateErr {
		holes++
	}
	fmt.Printf("Total hole runs in scanned range: %d\n", holes)
}

func main() {
	dir := flag.String("dir", `d:\n42-eth\chain\freezer`, "freezer directory")
	scanLo := flag.Uint64("scan-lo", 0, "start of linear-scan range (item index)")
	scanHi := flag.Uint64("scan-hi", 0, "end of linear-scan range (item index)")
	flag.Parse()

	tbl, err := freezer.NewFreezerTableCompressedReadOnly(*dir, "leaves", "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer tbl.Close()
	tbl.ForceBatchSize(freezer.BatchSize)

	declared := tbl.Items()
	fmt.Printf("cidx declared items: %d\n", declared)
	if declared == 0 {
		return
	}

	// Spot-check known suspects from prior failures + endpoints.
	suspects := []uint64{0, 6999999, 7013247, 7013248, 7013311, 7013312,
		7013376, 7100000, 7500000, 8000000, declared - 1}
	fmt.Println("\nspot-check (item → status):")
	for _, idx := range suspects {
		if idx >= declared {
			continue
		}
		_, err := tbl.Retrieve(idx)
		status := "OK"
		if err != nil {
			status = err.Error()
		}
		fmt.Printf("  %10d  %s\n", idx, status)
	}

	if *scanHi > *scanLo {
		scanRange(tbl, *scanLo, *scanHi)
		return
	}

	fmt.Println("\nNo --scan-lo/--scan-hi range given. Use a range scan to")
	fmt.Println("find interior holes — binary search is unreliable because")
	fmt.Println("the leaves table can have OK→FAIL→OK gaps after a crash.")
}
