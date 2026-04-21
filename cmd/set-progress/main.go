// set-progress writes the ethel progress marker to MDBX.
// Usage: set-progress --datadir D:\path --block 10430975
package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
)

func main() {
	if len(os.Args) < 5 || os.Args[1] != "--datadir" || os.Args[3] != "--block" {
		fmt.Fprintln(os.Stderr, "usage: set-progress --datadir PATH --block NUM")
		os.Exit(1)
	}
	datadir := os.Args[2]
	block, err := strconv.ParseUint(os.Args[4], 10, 64)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad block:", err)
		os.Exit(1)
	}

	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).Path(datadir).Label(kv.ChainDB).Accede().Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin rw:", err)
		os.Exit(1)
	}

	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], block)
	if err := tx.Put(kv.SyncStageProgress, []byte("ethel-last-block"), buf[:]); err != nil {
		tx.Rollback()
		fmt.Fprintln(os.Stderr, "put:", err)
		os.Exit(1)
	}
	if err := tx.Commit(); err != nil {
		fmt.Fprintln(os.Stderr, "commit:", err)
		os.Exit(1)
	}
	fmt.Printf("Progress set to block %d\n", block)
}
