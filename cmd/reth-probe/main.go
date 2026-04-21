// reth-probe: dump first N entries of a reth MDBX table to understand layout.
package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func tableCfg(tbls []string, dupsort map[string]bool) func(kv.TableCfg) kv.TableCfg {
	return func(defaults kv.TableCfg) kv.TableCfg {
		for _, t := range tbls {
			it := kv.TableCfgItem{}
			if dupsort[t] {
				it.Flags = kv.DupSort
			}
			defaults[t] = it
		}
		return defaults
	}
}

func main() {
	dbPath := flag.String("db", `d:\reth2k\db`, "reth MDBX path")
	table := flag.String("table", "StorageChangeSets", "table name")
	n := flag.Int("n", 5, "rows to print")
	dup := flag.Bool("dup", true, "dupsort")
	seekHex := flag.String("seek", "", "hex seek key (if set)")
	flag.Parse()

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).
		Path(*dbPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(4 * datasize.TB).
		Readonly().
		WithTableCfg(tableCfg([]string{*table}, map[string]bool{*table: *dup})).
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

	c, err := tx.Cursor(*table)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cursor:", err)
		os.Exit(1)
	}
	defer c.Close()

	var k, v []byte
	if *seekHex != "" {
		seek, _ := hex.DecodeString(*seekHex)
		k, v, err = c.Seek(seek)
	} else {
		k, v, err = c.First()
	}
	count := 0
	for ; k != nil && count < *n; k, v, err = c.Next() {
		if err != nil {
			fmt.Fprintln(os.Stderr, "iter:", err)
			break
		}
		// Print key as hex; also interpret first 8B BE as block if len >= 8.
		if len(k) >= 8 {
			fmt.Printf("#%d k_len=%d k=%x (firstBE=%d) v_len=%d v=%x\n",
				count, len(k), k, binary.BigEndian.Uint64(k[:8]), len(v), v)
		} else {
			fmt.Printf("#%d k_len=%d k=%x v_len=%d v=%x\n", count, len(k), k, len(v), v)
		}
		count++
	}
	stats, err := tx.BucketSize(*table)
	if err == nil {
		fmt.Printf("table=%s bucket_size=%d\n", *table, stats)
	}
}
