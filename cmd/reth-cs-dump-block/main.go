// reth-cs-dump-block dumps all AccountChangeSet entries for a single block.
// Verifies DupSort iteration sees every duplicate (catches the case where
// cursor.Next() in reth-acct-history might have skipped values).
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

const tbl = "AccountChangeSets"

func cfg(d kv.TableCfg) kv.TableCfg {
	d[tbl] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

func main() {
	rethDir := flag.String("reth", `d:\reth2k\db`, "reth MDBX path")
	block := flag.Uint64("block", 0, "block number")
	flag.Parse()

	logger := log.New()
	db, _ := mdbx.NewMDBX(logger).Path(*rethDir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(cfg).Open(context.Background())
	defer db.Close()
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()

	cur, _ := tx.CursorDupSort(tbl)
	defer cur.Close()

	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, *block)
	_, v, err := cur.SeekExact(key)
	if err != nil || v == nil {
		fmt.Fprintf(os.Stderr, "block %d not found in reth changesets (err=%v)\n", *block, err)
		os.Exit(1)
	}
	count := 0
	fmt.Printf("block %d AccountChangeSet entries:\n", *block)
	for v != nil {
		if len(v) >= 20 {
			fmt.Printf("  [%3d] addr=0x%x prevLen=%d prev=0x%x\n",
				count, v[:20], len(v)-20, v[20:])
		}
		count++
		_, v, err = cur.NextDup()
		if err != nil {
			break
		}
	}
	fmt.Printf("total dup-values: %d\n", count)
}
