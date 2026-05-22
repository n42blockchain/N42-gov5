// n42-chaindata-drop-dense removes the G2 dense legacy tables from
// D:\n42-chaindata. Reclaims ~58 GB of MDBX free pages (the .dat
// file does NOT shrink without env-copy compact; the freed pages
// become available for reuse by other tables).
//
// Safe to run any time — current RB-1..4 production path reads
// from D:\reth2k\db directly and doesn't depend on these tables.
//
// Usage:
//
//	n42-chaindata-drop-dense --dir D:\n42-chaindata
//	n42-chaindata-drop-dense --dir D:\n42-chaindata --dry-run
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

var legacyTables = []string{
	"AccountsDense",
	"StoragesDense",
	"AccountsDenseV2",
	"StoragesDenseV2",
}

func main() {
	dir := flag.String("dir", `D:\n42-chaindata`, "chaindata env dir")
	dryRun := flag.Bool("dry-run", false, "report what would be dropped without modifying anything")
	flag.Parse()

	if _, err := os.Stat(*dir); err != nil {
		fmt.Fprintf(os.Stderr, "dir %s not present: %v\n", *dir, err)
		os.Exit(1)
	}

	logger := log.New()
	ctx := context.Background()

	db, err := mdbxkv.NewMDBX(logger).
		Path(*dir).Label(kv.ChainDB).PageSize(4096).
		MapSize(4 * datasize.TB).
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			for _, t := range legacyTables {
				d[t] = kv.TableCfgItem{}
			}
			return d
		}).Open(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open env: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if *dryRun {
		roTx, _ := db.BeginRo(ctx)
		defer roTx.Rollback()
		fmt.Println("--- dry-run: tables that would be dropped ---")
		for _, t := range legacyTables {
			c, cerr := roTx.Cursor(t)
			if cerr != nil {
				fmt.Printf("  %s: cursor err %v\n", t, cerr)
				continue
			}
			n := uint64(0)
			for k, _, e := c.First(); k != nil; k, _, e = c.Next() {
				if e != nil {
					break
				}
				n++
			}
			c.Close()
			fmt.Printf("  %-20s entries=%d\n", t, n)
		}
		fmt.Println("--- (no changes made) ---")
		return
	}

	rwTx, err := db.BeginRw(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "BeginRw: %v\n", err)
		os.Exit(1)
	}
	for _, t := range legacyTables {
		if cerr := rwTx.ClearBucket(t); cerr != nil {
			fmt.Fprintf(os.Stderr, "ClearBucket %s: %v\n", t, cerr)
			rwTx.Rollback()
			os.Exit(1)
		}
		fmt.Printf("cleared %s\n", t)
	}
	if cerr := rwTx.Commit(); cerr != nil {
		fmt.Fprintf(os.Stderr, "commit: %v\n", cerr)
		os.Exit(1)
	}
	fmt.Println("done. Pages freed to MDBX free-list (mdbx.dat file size unchanged;")
	fmt.Println("run env-copy compact to physically reclaim disk).")
}
