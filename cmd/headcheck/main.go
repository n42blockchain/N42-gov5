// One-shot read of the eth-el head pointer from chaindata.
// Useful as a low-noise probe while the downloader is in flight —
// avoids polluting the eth-el log with extra noise.

package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/log"
)

func main() {
	dir := flag.String("dir", "D:/N42-eth1177-test/chaindata", "chaindata path")
	flag.Parse()
	db, err := mdbx.NewMDBX(log.New()).Path(*dir).Readonly().Open()
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Println("begin:", err)
		os.Exit(1)
	}
	defer tx.Rollback()
	v, _ := tx.GetOne(kv.SyncStageProgress, []byte("ethel-last-block"))
	if len(v) != 8 {
		fmt.Println("no head, len=", len(v))
		return
	}
	head := binary.BigEndian.Uint64(v)
	fmt.Printf("ethel-last-block = %d\n", head)
}
