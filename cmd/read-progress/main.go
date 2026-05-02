// read-progress: print the ethel progress marker for a datadir.
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
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: read-progress --dir <DIR>")
		os.Exit(1)
	}
	logger := log.New()
	db, err := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB).Accede().Readonly().Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "tx:", err)
		os.Exit(1)
	}
	defer tx.Rollback()
	v, err := tx.GetOne(kv.SyncStageProgress, []byte("ethel-last-block"))
	if err != nil || v == nil || len(v) < 8 {
		fmt.Printf("no progress marker (err=%v len=%d)\n", err, len(v))
		os.Exit(0)
	}
	fmt.Printf("progress: block %d\n", binary.BigEndian.Uint64(v))
}
