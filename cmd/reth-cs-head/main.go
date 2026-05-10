// reth-cs-head finds the last block number recorded in AccountChangeSets
// and StorageChangeSets in a reth MDBX database.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func main() {
	dbPath := flag.String("db", `d:\reth2k\db`, "reth MDBX path")
	flag.Parse()

	tables := []string{"AccountChangeSets", "StorageChangeSets"}
	logger := log.New()
	db, err := mdbx.NewMDBX(logger).
		Path(*dbPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(4 * datasize.TB).
		Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			for _, t := range tables {
				d[t] = kv.TableCfgItem{Flags: kv.DupSort}
			}
			return d
		}).
		Open(context.Background())
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

	for _, t := range tables {
		c, err := tx.Cursor(t)
		if err != nil {
			fmt.Println(t, "cursor err:", err)
			continue
		}
		k, _, err := c.Last()
		if err != nil {
			fmt.Println(t, "last err:", err)
			c.Close()
			continue
		}
		var blk uint64
		if len(k) >= 8 {
			blk = binary.BigEndian.Uint64(k[:8])
		}
		fmt.Printf("%s last_block=%d  key=%x\n", t, blk, k)
		c.Close()
	}
}
