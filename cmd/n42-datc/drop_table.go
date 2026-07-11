// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// drop-table — drops one named table from a DATC MDBX env. Used after a
// table's history migrates to static segments (e.g. DatcStoRoot → sr.*.seg):
// the freed pages return to the env freelist (the file does not shrink; a
// subsequent compact copy reclaims the bytes physically).
//
// Deliberately requires --yes: this is the destructive moment, and the caller
// is expected to have verified the replacement (spot check + bench) first.
package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func runDropTable(args []string) {
	fs := flag.NewFlagSet("drop-table", flag.ExitOnError)
	out := fs.String("out", "", "DATC MDBX dir")
	table := fs.String("table", "", "table to drop")
	mapGB := fs.Int("map.gb", 2048, "MDBX map GB")
	yes := fs.Bool("yes", false, "confirm the drop (refuses without it)")
	_ = fs.Parse(args)
	if *out == "" || *table == "" {
		die("--out and --table required")
	}
	if !*yes {
		die("refusing to drop %s without --yes (verify the replacement first)", *table)
	}

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(log.New()).Path(*out).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).Accede().Open(context.Background())
	if err != nil {
		die("open: %v", err)
	}
	defer db.Close()

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		die("begin: %v", err)
	}
	defer tx.Rollback()

	st, err := tx.(*mdbxkv.MdbxTx).BucketStat(*table)
	if err != nil || st == nil {
		die("stat %s: %v", *table, err)
	}
	fmt.Printf("dropping %s: %d rows, %.1f GB of pages\n",
		*table, st.Entries, float64((st.LeafPages+st.BranchPages+st.OverflowPages)*4096)/(1<<30))

	t0 := time.Now()
	if err := tx.DropBucket(*table); err != nil {
		die("drop: %v", err)
	}
	if err := tx.Commit(); err != nil {
		die("commit: %v", err)
	}
	fmt.Printf("dropped %s in %s (pages returned to the freelist; run a compact copy to reclaim physically)\n",
		*table, time.Since(t0).Truncate(time.Millisecond))
}
