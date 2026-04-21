// storage-scan — iterate Storage table and find entries with > 32 byte values.
// These are unambiguous corruption (uint256 storage slot max is 32 bytes).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func main() {
	datadir := flag.String("datadir", "", "MDBX datadir")
	limit := flag.Int("limit", 200, "max offending entries to print")
	flag.Parse()

	if *datadir == "" {
		fmt.Fprintln(os.Stderr, "usage: storage-scan --datadir <path>")
		os.Exit(1)
	}

	modules.N42Init()
	for name, cfg := range modules.N42TableCfg {
		kv.ChaindataTablesCfg[name] = cfg
	}

	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).Path(*datadir).Label(kv.ChainDB).Readonly().Accede().Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	cur, err := tx.Cursor(modules.Storage)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cursor:", err)
		os.Exit(1)
	}
	defer cur.Close()

	var total, offending uint64
	for k, v, err := cur.First(); k != nil; k, v, err = cur.Next() {
		if err != nil {
			fmt.Fprintln(os.Stderr, "iter err:", err)
			os.Exit(1)
		}
		total++
		if len(v) > 32 {
			offending++
			if offending <= uint64(*limit) {
				fmt.Printf("offending len=%d key=%x val=%x\n", len(v), k, v)
			}
		}
	}
	fmt.Printf("\nsummary: total=%d offending(>32B)=%d\n", total, offending)
}
