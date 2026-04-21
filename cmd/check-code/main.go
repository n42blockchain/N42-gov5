// check-code prints the Code table row count for a datadir's MDBX.
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
	dir := flag.String("dir", "", "datadir (MDBX path)")
	flag.Parse()

	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: check-code --dir <MDBX_DIR>")
		os.Exit(1)
	}

	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB).Accede().Readonly().Open(context.Background())
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

	sz, err := tx.BucketSize(modules.Code)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bucket size:", err)
		os.Exit(1)
	}

	c, err := tx.Cursor(modules.Code)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cursor:", err)
		os.Exit(1)
	}
	defer c.Close()
	count := uint64(0)
	for k, _, err := c.First(); k != nil; k, _, err = c.Next() {
		if err != nil {
			fmt.Fprintln(os.Stderr, "iter:", err)
			os.Exit(1)
		}
		count++
	}
	fmt.Printf("datadir: %s\nCode table rows: %d  bytes: %d\n", *dir, count, sz)
}
