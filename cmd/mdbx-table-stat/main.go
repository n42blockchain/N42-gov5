// mdbx-table-stat opens a reth/erigon/N42 MDBX database read-only and
// prints (entries, page-count, byte-size) for the named tables. Use it
// to count active leaves before sizing a state-commitment scheme.
//
// Reth tables (D:\reth2k\db at ~25M blocks):
//
//	Account              — current account state (key=addr20)
//	Storage              — current storage (DupSort, key=addr20, subkey=slot32)
//	AccountChangeSets    — historical account writes
//	StorageChangeSets    — historical storage writes
//	AccountsHistory      — inverted index addr → modified blocks
//	StoragesHistory      — inverted index (addr,slot) → modified blocks
//	Bytecodes            — contract code by keccak
//
// The Account/Storage "entries" count is what you want for active-leaf
// counting; the *ChangeSets entries should match cumulative-write totals.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

var defaultTables = []string{
	"Account",
	"Storage",
	"AccountChangeSets",
	"StorageChangeSets",
	"AccountsHistory",
	"StoragesHistory",
	"Bytecodes",
}

func tableCfg(tables []string) func(kv.TableCfg) kv.TableCfg {
	return func(d kv.TableCfg) kv.TableCfg {
		for _, t := range tables {
			d[t] = kv.TableCfgItem{}
		}
		return d
	}
}

func main() {
	dbPath := flag.String("db", `D:\reth2k\db`, "MDBX database directory")
	tablesCSV := flag.String("tables", strings.Join(defaultTables, ","), "comma-separated tables")
	mapSizeGB := flag.Int("mapsize-gb", 4096, "MDBX MapSize cap (must >= actual db size)")
	flag.Parse()

	tables := strings.Split(*tablesCSV, ",")
	for i := range tables {
		tables[i] = strings.TrimSpace(tables[i])
	}

	logger := log.New()
	t0 := time.Now()
	db, err := mdbxkv.NewMDBX(logger).
		Path(*dbPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(datasize.ByteSize(*mapSizeGB) * datasize.GB).
		Readonly().
		WithTableCfg(tableCfg(tables)).
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

	mtx := tx.(*mdbxkv.MdbxTx)

	fmt.Printf("mdbx-table-stat — %s  open=%s\n\n", *dbPath, time.Since(t0).Truncate(time.Millisecond))
	fmt.Printf("%-22s %16s %14s %14s %14s\n",
		"table", "entries", "leaf_pages", "branch_pages", "size")
	fmt.Println(strings.Repeat("-", 86))

	for _, t := range tables {
		st, err := mtx.BucketStat(t)
		if err != nil {
			fmt.Printf("%-22s  ERROR: %v\n", t, err)
			continue
		}
		size := (st.LeafPages + st.BranchPages + st.OverflowPages) * 4096
		fmt.Printf("%-22s %16d %14d %14d %14s\n",
			t, st.Entries, st.LeafPages, st.BranchPages,
			datasize.ByteSize(size).HR())
	}
}
