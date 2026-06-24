// Throwaway: probe an MDBX datadir for erigon-style AccountChangeSet /
// StorageChangeSet coverage — min/max block and whether a target gap range has
// entries. Read-only. Used to find a datadir that can regenerate missing
// changesets for a specific height range.
package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: cs-probe <datadir> [gapLo gapHi]")
		os.Exit(1)
	}
	dir := os.Args[1]
	var gapLo, gapHi uint64 = 25101824, 25101866
	if len(os.Args) >= 4 {
		fmt.Sscan(os.Args[2], &gapLo)
		fmt.Sscan(os.Args[3], &gapHi)
	}
	logger := log.New()
	db, err := mdbxkv.NewMDBX(logger).Path(dir).Label(kv.ChainDB).
		MapSize(8 * datasize.TB).Readonly().Accede().Open(context.Background())
	if err != nil {
		fmt.Printf("OPEN FAIL: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Printf("begin: %v\n", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	blk := func(k []byte) uint64 {
		if len(k) < 8 {
			return 0
		}
		return binary.BigEndian.Uint64(k[:8])
	}
	probe := func(table string) {
		c, err := tx.Cursor(table)
		if err != nil {
			fmt.Printf("  %-18s cursor err: %v\n", table, err)
			return
		}
		defer c.Close()
		fk, _, _ := c.First()
		lk, _, _ := c.Last()
		if fk == nil {
			fmt.Printf("  %-18s EMPTY\n", table)
			return
		}
		// count entries inside the gap range
		inGap := 0
		var seekKey [8]byte
		binary.BigEndian.PutUint64(seekKey[:], gapLo)
		for k, _, e := c.Seek(seekKey[:]); k != nil && e == nil; k, _, e = c.Next() {
			b := blk(k)
			if b > gapHi {
				break
			}
			inGap++
		}
		fmt.Printf("  %-18s min=%d  max=%d  entries-in-gap[%d,%d]=%d\n",
			table, blk(fk), blk(lk), gapLo, gapHi, inGap)
	}
	fmt.Printf("== %s ==\n", dir)
	probe(modules.AccountChangeSet)
	probe(modules.StorageChangeSet)
}
