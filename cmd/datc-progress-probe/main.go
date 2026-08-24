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

	for _, k := range []string{"progress", "leafprog", "head"} {
		v, _ := tx.GetOne("DatcMeta", []byte(k))
		if len(v) >= 8 {
			fmt.Printf("meta %-10s = %d\n", k, binary.BigEndian.Uint64(v[:8]))
		}
	}
	for _, tbl := range []string{"DatcAccNode", "DatcStorNode", "DatcAccChg", "DatcStorChg", "DatcLeafA", "DatcLeafS", "TrieAccount", "TrieStorage"} {
		c, err := tx.Cursor(tbl)
		if err != nil {
			fmt.Printf("%-14s cursor err: %v\n", tbl, err)
			continue
		}
		n, err := c.Count()
		if err != nil {
			fmt.Printf("%-14s count err: %v\n", tbl, err)
			c.Close()
			continue
		}
		fk, _, _ := c.First()
		lk, _, _ := c.Last()
		fmt.Printf("%-14s count=%-12d firstKeyLen=%d lastKeyLen=%d firstKey=%x\n", tbl, n, len(fk), len(lk), fk)
		c.Close()
	}

	// Decisive key-schema check: does flushStoLevel's 40-byte (addrHash+inc)
	// key match the real TrieStorage 32-byte (no-incarnation) key?
	{
		c, _ := tx.Cursor("TrieStorage")
		fk, _, _ := c.First()
		c.Close()
		if len(fk) >= 32 {
			ah := fk[:32]
			v32, _ := tx.GetOne("TrieStorage", ah)
			dom40 := append(append([]byte{}, ah...), make([]byte, 8)...) // +inc(0)
			v40, _ := tx.GetOne("TrieStorage", dom40)
			fmt.Printf("\nkey-schema check on addrHash=%x\n", ah)
			fmt.Printf("  GetOne(32B addrHash)        -> %d bytes (hit=%v)\n", len(v32), len(v32) > 0)
			fmt.Printf("  GetOne(40B addrHash+inc0)   -> %d bytes (hit=%v)  <- what flushStoLevel reads\n", len(v40), len(v40) > 0)
		}
	}
}
