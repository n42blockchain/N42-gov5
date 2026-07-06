package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func main() {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(log.New()).Path(os.Args[1]).Label(kv.ChainDB).
		MapSize(2*datasize.TB).Readonly().Open(context.Background())
	if err != nil {
		fmt.Println("OPEN", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Println("TX", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	for _, arg := range os.Args[2:] {
		n, _ := strconv.ParseUint(arg, 10, 64)
		blk, err := rawdb.ReadBlockByNumber(tx, n)
		if err != nil || blk == nil {
			fmt.Printf("block %d: read error %v\n", n, err)
			continue
		}
		ts := blk.Time()
		t := time.Unix(int64(ts), 0).UTC()
		fmt.Printf("block %-12d time=%-12d utc=%s\n", n, ts, t.Format("2006-01-02 15:04:05"))
	}
}
