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
	db, err := mdbxkv.NewMDBX(log.New()).Path(os.Args[1]).Label(kv.ChainDB).MapSize(4096 * datasize.GB).Accede().Readonly().Open(context.Background())
	if err != nil {
		panic(err)
	}
	defer db.Close()
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()
	mtx := tx.(*mdbxkv.MdbxTx)
	type row struct {
		name string
		n    uint64
		gb   float64
	}
	var rows []row
	tables, _ := mtx.ListBuckets()
	for _, t := range tables {
		st, err := mtx.BucketStat(t)
		if err != nil || st == nil {
			continue
		}
		sz := float64((st.LeafPages+st.BranchPages+st.OverflowPages)*4096) / (1 << 30)
		if sz > 0.05 {
			rows = append(rows, row{t, st.Entries, sz})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].gb > rows[j].gb })
	for _, r := range rows {
		fmt.Printf("%-22s %14d rows  %8.1f GB\n", r.name, r.n, r.gb)
	}
}
