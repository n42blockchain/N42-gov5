// freezer-info: dump items() per table for a freezer dir, both raw
// and compressed where applicable. Used to spot height drift between
// inputs (headers/bodies) and outputs (witness/acctcs/storcs/senders).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

var compactTables = []string{
	freezer.TableHeaders, freezer.TableBodies, freezer.TableReceipts,
	freezer.TableSenders, freezer.TableAccountChanges, freezer.TableStorageChanges,
	freezer.TableLeavesJournal, freezer.TableBlockWitness,
}

func main() {
	dir := flag.String("dir", "", "freezer dir")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: freezer-info --dir <freezer>")
		os.Exit(1)
	}

	fmt.Printf("Freezer: %s\n\n", *dir)

	// Probe via individual table opens — works regardless of which
	// tables exist. We try compressed (.cdat / .cidx) first, then
	// raw (.dat / .ridx) for a heads-up if both layouts coexist.
	maxNameLen := 0
	for _, t := range compactTables {
		if len(t) > maxNameLen {
			maxNameLen = len(t)
		}
	}

	for _, name := range compactTables {
		cidx := filepath.Join(*dir, name+".cidx")
		ridx := filepath.Join(*dir, name+".ridx")
		switch {
		case fileExists(cidx):
			t, err := freezer.NewFreezerTableCompressedReadOnly(*dir, name, "c")
			if err != nil {
				fmt.Printf("  %-*s [c] open error: %v\n", maxNameLen, name, err)
				continue
			}
			fmt.Printf("  %-*s [c] items=%d\n", maxNameLen, name, t.Items())
			t.Close()
		case fileExists(ridx):
			fmt.Printf("  %-*s [r] index present (.ridx) — use freezer.New for raw read\n", maxNameLen, name)
		default:
			// not present
		}
	}

	// Also probe N42 columnar (headerc/bodyc) if present.
	headercIdx := filepath.Join(*dir, "headerc.cidx")
	if fileExists(headercIdx) {
		st, _ := os.Stat(headercIdx)
		segments := st.Size() / 8
		fmt.Printf("\n  headerc.cidx: %d segments × 8192 = %d max blocks\n",
			segments, segments*8192)
	}
	bodycIdx := filepath.Join(*dir, "bodyc.cidx")
	if fileExists(bodycIdx) {
		st, _ := os.Stat(bodycIdx)
		segments := st.Size() / 8
		fmt.Printf("  bodyc.cidx: %d segments × 8192 = %d max blocks\n",
			segments, segments*8192)
	}

	// Geth-style probe via freezer.New (for ancient dirs).
	if !strings.HasSuffix(*dir, "freezer") {
		gf, err := freezer.NewReadOnly(*dir)
		if err == nil {
			fmt.Printf("\n  geth-style freezer.Frozen() = %d\n", gf.Frozen())
			gf.Close()
		}
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
