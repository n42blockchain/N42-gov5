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
		fmt.Fprintln(os.Stderr, "usage: set-progress --datadir PATH --block NUM [--key NAME]")
		fmt.Fprintln(os.Stderr, "  --key: SyncStageProgress key (default ethel-last-block;")
		fmt.Fprintln(os.Stderr, "         e.g. ethel-merkle-block for the staged Merkle marker)")
		os.Exit(1)
	}
	datadir := os.Args[2]
	key := "ethel-last-block"
	if len(os.Args) >= 7 && os.Args[5] == "--key" {
		key = os.Args[6]
	}
	block, err := strconv.ParseUint(os.Args[4], 10, 64)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad block:", err)
		os.Exit(1)
	}

	logger := log2.New()
	// No Accede: allow seeding a BRAND-NEW chaindata (e.g. a snapshot-direct
	// start kit whose only content is the ethel-last-block=H0 marker).
	db, err := mdbx.NewMDBX(logger).Path(datadir).Label(kv.ChainDB).Open(context.Background())
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
	if err := tx.Put(kv.SyncStageProgress, []byte(key), buf[:]); err != nil {
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
