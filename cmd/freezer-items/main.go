// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// freezer-items: print the Items() count for every standard table in a
// freezer directory. STRICTLY READ-ONLY.
//
// History: this tool used to call freezer.New() (RW) + EnsureTable(),
// which created 16-byte CIDX-header stubs for any extended table that
// didn't exist on the target dir — most visibly when run against geth's
// own chaindata/ancient/chain (where acctcs/senders/storcs/witness are
// not present). New() also runs alignOnResume which can truncate tables
// down to the smallest item count of any opened table.
//
// We now use NewReadOnly which:
//   - opens cidx files O_RDONLY,
//   - skips alignOnResume / cross-table truncation,
//   - never creates new files.
//
// This is a diagnostic — it must never write.

package main

import (
	"fmt"
	"os"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: freezer-items <freezer-dir>")
		os.Exit(1)
	}
	dir := os.Args[1]
	f, err := freezer.NewReadOnly(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open freezer (read-only): %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	// Only the 6-byte CompactTable cidx tables are reported here. The
	// freezer.FreezerTable code path that backs Items() hardcodes a
	// 6-byte entry size (modules/rawdb/freezer/table.go: indexEntrySize).
	// bodies/headers use 8-byte cidx (encodeBodyIdx in body_compact.go);
	// accthist/storhist/txindex use 12-byte cidx (cscompact/segment_store.go).
	// Reporting Items() on those would silently divide by 6 and produce
	// nonsense counts. Use `ethexec db-stats` for those — it switches the
	// divisor by table name.
	for _, name := range []string{"acctcs", "storcs", "senders", "witness", "leaves", "receipts"} {
		// Table() returns nil for tables whose cidx didn't exist at open
		// time. We do NOT EnsureTable here — that would create a 16-byte
		// stub on disk and (in future RW opens against this dir) could
		// trigger alignOnResume.
		tbl := f.Table(name)
		if tbl == nil {
			fmt.Printf("%-10s (absent)\n", name)
			continue
		}
		items := tbl.Items()
		if items == 0 {
			fmt.Printf("%-10s items=0\n", name)
			continue
		}
		fmt.Printf("%-10s items=%d highest_block=%d\n", name, items, items-1)
	}
	fmt.Fprintln(os.Stderr, "\nNOTE: bodies/headers (8 B cidx) and accthist/storhist/txindex")
	fmt.Fprintln(os.Stderr, "(12 B SegmentStore cidx) are NOT shown — use `ethexec db-stats` for those.")
}
