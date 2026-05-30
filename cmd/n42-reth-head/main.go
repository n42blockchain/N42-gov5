// n42-reth-head reports the canonical head block number of a reth MDBX db,
// needed as H0 for snapshot-direct bootstrap (snapshot segment naming +
// ethel-last-block marker + catchup start). Read-only.
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

// reth canonical/header tables whose key is an 8-byte BE block number; the last
// key gives the head height. Probe several so we don't depend on one name.
var candidates = []string{
	"CanonicalHeaders",
	"Headers",
	"HeaderNumbers",
	"BlockBodyIndices",
	"HeaderTerminalDifficulties",
}

func cfg(d kv.TableCfg) kv.TableCfg {
	for _, t := range candidates {
		d[t] = kv.TableCfgItem{}
	}
	return d
}

func main() {
	rethDir := flag.String("reth", `D:/reth2k/db`, "reth db (read-only)")
	flag.Parse()

	logger := log.New()
	rdb, err := mdbx.NewMDBX(logger).Path(*rethDir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(cfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open reth:", err)
		os.Exit(1)
	}
	defer rdb.Close()
	tx, _ := rdb.BeginRo(context.Background())
	defer tx.Rollback()

	for _, t := range candidates {
		c, err := tx.Cursor(t)
		if err != nil {
			fmt.Printf("%-28s : (open err: %v)\n", t, err)
			continue
		}
		k, _, err := c.Last()
		c.Close()
		if err != nil || k == nil {
			fmt.Printf("%-28s : (empty / %v)\n", t, err)
			continue
		}
		if len(k) >= 8 {
			fmt.Printf("%-28s : lastKey block = %d (keyLen=%d, key=%x)\n",
				t, binary.BigEndian.Uint64(k[:8]), len(k), k)
		} else {
			fmt.Printf("%-28s : lastKey len=%d key=%x\n", t, len(k), k)
		}
	}
}
