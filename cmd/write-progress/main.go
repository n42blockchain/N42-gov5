// write-progress: set the ethel progress marker for a datadir.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func main() {
	dir := flag.String("dir", "", "datadir")
	target := flag.Uint64("target", 0, "block number")
	create := flag.Bool("create", false, "create the database if absent (fresh snapshot-direct datadir bootstrap)")
	flag.Parse()
	if *dir == "" || *target == 0 {
		fmt.Fprintln(os.Stderr, "usage: write-progress --dir <DIR> --target <BLOCK> [--create]")
		os.Exit(1)
	}
	logger := log.New()
	opts := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB)
	if !*create {
		opts = opts.Accede()
	}
	db, err := opts.Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "tx:", err)
		os.Exit(1)
	}
	defer tx.Rollback()
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], *target)
	if err := tx.Put(kv.SyncStageProgress, []byte("ethel-last-block"), buf[:]); err != nil {
		fmt.Fprintln(os.Stderr, "put:", err)
		os.Exit(1)
	}
	if err := tx.Commit(); err != nil {
		fmt.Fprintln(os.Stderr, "commit:", err)
		os.Exit(1)
	}
	fmt.Printf("progress set to block %d\n", *target)
}
