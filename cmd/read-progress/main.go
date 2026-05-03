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
	// Try both progress marker locations:
	//   - SyncStageProgress / "ethel-last-block"  (used by forward replay)
	//   - DbInfo / "ethel_progress"               (used by rebuild-state)
	v, err := tx.GetOne(kv.SyncStageProgress, []byte("ethel-last-block"))
	if err == nil && v != nil && len(v) >= 8 {
		fmt.Printf("SyncStageProgress/ethel-last-block: %d\n", binary.BigEndian.Uint64(v))
	} else {
		fmt.Printf("SyncStageProgress/ethel-last-block: (none)\n")
	}
	v2, err2 := tx.GetOne(kv.DatabaseInfo, []byte("ethel_progress"))
	if err2 == nil && v2 != nil && len(v2) >= 8 {
		fmt.Printf("DbInfo/ethel_progress:               %d\n", binary.BigEndian.Uint64(v2))
	} else {
		fmt.Printf("DbInfo/ethel_progress:               (none)\n")
	}
}
