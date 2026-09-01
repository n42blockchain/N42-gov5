// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// prep-state — turn a copied MDBX that holds the chain state at some block
// (HashedAccounts/HashedStorage/TrieOfAccounts/TrieOfStorage + a DatcMeta
// "progress") into a clean v2 build output: every DATC table is cleared and
// the foreign meta keys dropped, so `build --out` auto-resumes from that
// block with the state as its incremental base. Used to run a second build
// process over the upper block range in parallel with the genesis one.
//
//	n42-datc prep-state --out /data/datc-25m-hi [--map.gb 4096]
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"

	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func runPrepState(args []string) {
	fs := flag.NewFlagSet("prep-state", flag.ExitOnError)
	out := fs.String("out", "", "MDBX dir holding the state tables (a COPY — tables are cleared in place)")
	mapGB := fs.Int("map.gb", 4096, "MDBX map size GB")
	dryRun := fs.Bool("dry-run", false, "report only")
	_ = fs.Parse(args)
	if *out == "" {
		die("--out required")
	}
	modulesInit()
	db, err := openDatcDB(log.New(), *out, *mapGB, 4)
	if err != nil {
		die("open: %v", err)
	}
	defer db.Close()
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		die("begin: %v", err)
	}
	defer tx.Rollback()

	pv, _ := tx.GetOne(tDatcMeta, []byte("progress"))
	if len(pv) != 8 {
		die("DatcMeta/progress missing — not a resumable state")
	}
	progress := binary.BigEndian.Uint64(pv)
	count := func(tab string) uint64 {
		c, err := tx.Cursor(tab)
		if err != nil {
			return 0
		}
		defer c.Close()
		n, _ := c.Count()
		return n
	}
	fmt.Printf("progress=%d (next block to build)\n", progress)
	for _, tab := range []string{modules.HashedAccounts, modules.HashedStorage, modules.TrieOfAccounts, modules.TrieOfStorage} {
		fmt.Printf("  keep  %-16s rows=%d\n", tab, count(tab))
	}
	clear := []string{tDatcAccNode, tDatcStoNode, tDatcStoRoot, tDatcRoots, tDatcLeafA, tDatcLeafS, tDatcAccChg, tDatcStoChg, tFwdAcctCS, tFwdStorCS}
	for _, tab := range clear {
		fmt.Printf("  clear %-16s rows=%d\n", tab, count(tab))
	}
	var drop [][]byte
	c, err := tx.Cursor(tDatcMeta)
	if err != nil {
		die("meta cursor: %v", err)
	}
	for k, _, e := c.First(); k != nil && e == nil; k, _, e = c.Next() {
		if string(k) != "progress" && string(k) != "leafprog" {
			drop = append(drop, append([]byte{}, k...))
		}
	}
	c.Close()
	fmt.Printf("  drop meta keys: %q\n", drop)
	if *dryRun {
		return
	}
	for _, tab := range clear {
		if err := tx.ClearBucket(tab); err != nil {
			die("clear %s: %v", tab, err)
		}
	}
	for _, k := range drop {
		if err := tx.Delete(tDatcMeta, k); err != nil {
			die("delete meta %s: %v", k, err)
		}
	}
	if err := tx.Commit(); err != nil {
		die("commit: %v", err)
	}
	fmt.Printf("prepared: build --out %s resumes at %d\n", *out, progress)
	_ = kv.ChainDB
}
