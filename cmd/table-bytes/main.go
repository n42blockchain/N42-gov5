// table-bytes — sum value bytes + count for given MDBX tables (DATC node records).
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

func main() {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(log.New()).Path(os.Args[1]).Label(kv.ChainDB).
		MapSize(8 * datasize.TB).Readonly().Accede().Open(context.Background())
	if err != nil {
		fmt.Println("OPEN", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()
	for _, t := range []string{"DatcAccNode", "DatcStorNode", "TrieAccount", "TrieStorage", "DatcRoots"} {
		c, err := tx.Cursor(t)
		if err != nil {
			fmt.Printf("%-14s err %v\n", t, err)
			continue
		}
		var n, kb, vb uint64
		for k, v, e := c.First(); k != nil && e == nil; k, v, e = c.Next() {
			n++
			kb += uint64(len(k))
			vb += uint64(len(v))
		}
		c.Close()
		var avg float64
		if n > 0 {
			avg = float64(kb+vb) / float64(n)
		}
		fmt.Printf("%-14s count=%-10d keyB=%-12d valB=%-13d total=%6.2fGB  avg=%.0fB/rec\n",
			t, n, kb, vb, float64(kb+vb)/1e9, avg)
	}
}
