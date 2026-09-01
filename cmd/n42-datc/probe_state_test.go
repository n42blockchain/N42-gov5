package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"testing"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// TestProbeStateDB (DATC_PROBE_DB=<dir>): read-only inventory of a foreign
// DATC/state MDBX — buckets, key lengths of the state tables, DatcMeta.
func TestProbeStateDB(t *testing.T) {
	dir := os.Getenv("DATC_PROBE_DB")
	if dir == "" {
		t.Skip("DATC_PROBE_DB not set")
	}
	modulesInit()
	db, err := mdbxkv.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).
		MapSize(4096 * datasize.GB).Accede().Readonly().
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg {
			d := kv.TableCfg{}
			for name, item := range kv.ChaindataTablesCfg {
				d[name] = item
			}
			for _, tname := range []string{"DatcAccNode", "DatcStorNode", "DatcStoRoot", "DatcMeta", "DatcLeafA", "DatcLeafS", "DatcAccChg", "DatcStorChg"} {
				d[tname] = kv.TableCfgItem{}
			}
			return d
		}).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	bs, _ := tx.ListBuckets()
	fmt.Printf("buckets: %v\n", bs)
	for _, tab := range []string{modules.HashedAccounts, modules.HashedStorage, modules.TrieOfAccounts, modules.TrieOfStorage, "DatcAccNode", "DatcStorNode", "DatcStoRoot"} {
		c, err := tx.Cursor(tab)
		if err != nil {
			fmt.Printf("%s: %v\n", tab, err)
			continue
		}
		k, v, _ := c.First()
		if k == nil {
			fmt.Printf("%-16s EMPTY\n", tab)
			c.Close()
			continue
		}
		fmt.Printf("%-16s firstKeyLen=%d vlen=%d key=%x\n", tab, len(k), len(v), k[:min(len(k), 12)])
		c.Close()
	}
	c, _ := tx.Cursor("DatcMeta")
	for k, v, e := c.First(); k != nil && e == nil; k, v, e = c.Next() {
		if len(v) == 8 {
			fmt.Printf("meta %s = %d\n", k, binary.BigEndian.Uint64(v))
		} else {
			fmt.Printf("meta %s = %x (%d bytes)\n", k, v[:min(len(v), 16)], len(v))
		}
	}
	c.Close()
}
