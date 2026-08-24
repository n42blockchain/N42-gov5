// Throwaway: count duplicate values per TrieStorage key in a DATC out MDBX.
// >1 dup at a key = stale node versions accumulated (DupSort append without
// delete-before-put) → GetOne returns the lexicographically-first (stale) one.
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

	c, err := tx.CursorDupSort("TrieStorage")
	if err != nil {
		fmt.Println("cursor", err)
		os.Exit(1)
	}
	defer c.Close()

	var keys, multiKeys, maxDup uint64
	var sampleKey []byte
	for k, _, e := c.First(); k != nil && e == nil; k, _, e = c.NextNoDup() {
		n, err := c.CountDuplicates()
		if err != nil {
			fmt.Println("countdup", err)
			break
		}
		keys++
		if n > 1 {
			multiKeys++
			if n > maxDup {
				maxDup = n
				sampleKey = append([]byte{}, k...)
			}
		}
	}
	fmt.Printf("TrieStorage keys=%d  keys-with->1-dup=%d  maxDupAtAKey=%d\n", keys, multiKeys, maxDup)
	if sampleKey != nil {
		fmt.Printf("sample multi-dup key=%x\n", sampleKey)
	}
}
