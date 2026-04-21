// show-progress reads the ethel progress marker from MDBX.
package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
)

func main() {
	if len(os.Args) < 3 || os.Args[1] != "--datadir" {
		fmt.Fprintln(os.Stderr, "usage: show-progress --datadir PATH")
		os.Exit(1)
	}
	datadir := os.Args[2]

	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).Path(datadir).Label(kv.ChainDB).Readonly().Accede().Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin ro:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	v, err := tx.GetOne(kv.SyncStageProgress, []byte("ethel-last-block"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "get:", err)
		os.Exit(1)
	}
	if len(v) == 0 {
		fmt.Printf("no progress marker\n")
		return
	}
	if len(v) != 8 {
		fmt.Printf("marker len=%d (unexpected): %x\n", len(v), v)
		return
	}
	b := binary.BigEndian.Uint64(v)
	fmt.Printf("ethel-last-block = %d\n", b)
}
