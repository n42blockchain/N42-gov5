package main

import (
	"context"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func loadKeys(dir, table string) map[string]struct{} {
	db, err := mdbxkv.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).MapSize(512 * datasize.GB).Accede().Readonly().Open(context.Background())
	if err != nil {
		panic(err)
	}
	defer db.Close()
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()
	out := map[string]struct{}{}
	c, err := tx.Cursor(table)
	if err != nil {
		panic(err)
	}
	for k, _, _ := c.First(); k != nil; k, _, _ = c.Next() {
		out[string(k)] = struct{}{}
	}
	c.Close()
	return out
}

// tabkeydiff <dirA> <dirB> <table>: print keys only in A / only in B (hex), plus counts.
func main() {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	a := loadKeys(os.Args[1], os.Args[3])
	b := loadKeys(os.Args[2], os.Args[3])
	na, nb := 0, 0
	for k := range a {
		if _, ok := b[k]; !ok {
			fmt.Printf("only-A %x\n", k)
			na++
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			fmt.Printf("only-B %x\n", k)
			nb++
		}
	}
	fmt.Printf("total: rowsA=%d rowsB=%d only-A=%d only-B=%d\n", len(a), len(b), na, nb)
}
