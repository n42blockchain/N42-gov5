package main

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func main() {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(log.New()).Path(os.Args[1]).Label(kv.ChainDB).
		MapSize(512 * datasize.GB).Accede().Readonly().Open(context.Background())
	if err != nil {
		panic(err)
	}
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()
	names, err := tx.ListBuckets()
	if err != nil {
		panic(err)
	}
	type ts struct {
		n string
		s uint64
	}
	var all []ts
	for _, n := range names {
		sz, err := tx.BucketSize(n)
		if err == nil && sz > 1<<20 {
			all = append(all, ts{n, sz})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].s > all[j].s })
	for i, e := range all {
		if i >= 12 {
			break
		}
		fmt.Printf("%-28s %8.2f GB\n", e.n, float64(e.s)/1e9)
	}
}
