// reth-cs-range prints the earliest and latest block number stored in
// reth's AccountChangeSets and StorageChangeSets MDBX tables. Used to
// detect reth's static-file pruning cutoff — anything below the earliest
// block has been migrated to static archives and is invisible to plain
// MDBX cursor scans.
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

func cfg(d kv.TableCfg) kv.TableCfg {
	d["AccountChangeSets"] = kv.TableCfgItem{Flags: kv.DupSort}
	d["StorageChangeSets"] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

func main() {
	rethDir := flag.String("reth", `d:\reth2k\db`, "reth MDBX path")
	flag.Parse()

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).
		Path(*rethDir).Label(kv.ChainDB).PageSize(4096).
		MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(cfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()

	for _, t := range []string{"AccountChangeSets", "StorageChangeSets"} {
		cur, _ := tx.Cursor(t)
		k, _, _ := cur.First()
		first := uint64(0)
		if len(k) >= 8 {
			first = binary.BigEndian.Uint64(k[:8])
		}
		k, _, _ = cur.Last()
		last := uint64(0)
		if len(k) >= 8 {
			last = binary.BigEndian.Uint64(k[:8])
		}
		cur.Close()
		fmt.Printf("%s: first_block=%d last_block=%d\n", t, first, last)
	}
}
