// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// freezer-heads prints every table's coverage in a freezer directory —
// the weekly top-up audit tool (read-only, near-zero memory).
package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: freezer-heads <freezer-dir>")
		os.Exit(2)
	}
	fz, err := freezer.NewReadOnly(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer fz.Close()
	fmt.Printf("%s  frozen=%d\n", os.Args[1], fz.Frozen())
	names := fz.TableNames()
	sort.Strings(names)
	for _, name := range names {
		t := fz.Table(name)
		if t == nil {
			continue
		}
		fmt.Printf("  %-10s start=%-10d items=%-10d\n", name, t.StartItem(), t.Items())
	}
}
