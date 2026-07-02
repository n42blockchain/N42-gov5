// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// freezer-retrim rewrites a per-item freezer table's cidx so the table
// logically begins mid-history (EIP-4444-style retention): entries below the
// keep-point are dropped and the new header records the absolute start item
// (cidx header bytes [8:16]). Data files are untouched — delete the cold
// .cdat files separately; reads below start return ErrPruned.
//
// Typical use: a "full" node that keeps only the recent window of receipts —
// delete the cold receipts.NNNN.cdat, then
//
//	freezer-retrim --dir <datadir>/chain/freezer --table receipts --keep-from-file 89
//
// The previous cidx is preserved as <table>.cidx.bak-retrim.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	dir := flag.String("dir", "", "freezer directory containing <table>.cidx")
	table := flag.String("table", "", "table name (e.g. receipts)")
	ext := flag.String("ext", "c", "index extension family (cidx => c)")
	keepFromFile := flag.Int("keep-from-file", -1, "first data-file number to keep (e.g. 89 keeps NNNN >= 0089)")
	startItem := flag.Uint64("start-item", 0, "alternative: absolute first item to keep (snapped back to a batch boundary)")
	dryrun := flag.Bool("dryrun", false, "report what would be dropped without writing")
	flag.Parse()

	if *dir == "" || *table == "" || (*keepFromFile < 0 && *startItem == 0) {
		flag.Usage()
		os.Exit(2)
	}

	if *dryrun {
		// Open read-only and report coverage so the operator can sanity-check.
		tbl, err := freezer.NewFreezerTableReadOnly(*dir, *table, *ext)
		if err != nil {
			fatal("open table: %v", err)
		}
		defer tbl.Close()
		fmt.Printf("table %s: start=%d items=%d files-present=%s\n",
			*table, tbl.StartItem(), tbl.Items(), presentFiles(*dir, *table))
		fmt.Println("dryrun: no changes written")
		return
	}

	var (
		newStart, dropped uint64
		err               error
	)
	if *keepFromFile >= 0 {
		newStart, dropped, err = freezer.RetrimIndexToFile(*dir, *table, *ext, uint16(*keepFromFile))
	} else {
		newStart, dropped, err = freezer.RetrimIndexToItem(*dir, *table, *ext, *startItem)
	}
	if err != nil {
		fatal("retrim: %v", err)
	}
	fmt.Printf("retrimmed %s: newStart=%d droppedEntries=%d (backup: %s.%sidx.bak-retrim)\n",
		*table, newStart, dropped, *table, *ext)

	// Verify the table reopens cleanly.
	tbl, err := freezer.NewFreezerTableReadOnly(*dir, *table, *ext)
	if err != nil {
		fatal("verify reopen: %v", err)
	}
	defer tbl.Close()
	fmt.Printf("verified: start=%d items=%d\n", tbl.StartItem(), tbl.Items())
}

// presentFiles summarizes which NNNN.cdat files exist, e.g. "0089..0091 (3)".
func presentFiles(dir, table string) string {
	matches, _ := filepath.Glob(filepath.Join(dir, table+".*.cdat"))
	var nums []int
	for _, m := range matches {
		parts := strings.Split(filepath.Base(m), ".")
		if len(parts) == 3 {
			if n, err := strconv.Atoi(parts[1]); err == nil {
				nums = append(nums, n)
			}
		}
	}
	if len(nums) == 0 {
		return "none"
	}
	sort.Ints(nums)
	return fmt.Sprintf("%04d..%04d (%d)", nums[0], nums[len(nums)-1], len(nums))
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
