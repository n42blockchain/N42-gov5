// reth-tables-list lists every MDBX table reth has and their entry
// counts. Used to discover tables we don't know about (e.g. TransactionSenders,
// AccountsHistory) that might tell us total tx count per address.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// We don't know the table names, so we open with empty cfg and let
// the WithTableCfgFn populate from the actual DB metadata.
var knownTables = []string{
	"PlainAccountState",
	"PlainStorageState",
	"AccountChangeSets",
	"StorageChangeSets",
	"AccountsHistory",
	"StoragesHistory",
	"TransactionSenders",
	"TransactionBlocks",
	"TxHashNumber",
	"BlockBodyIndices",
	"Bytecodes",
	"AccountsTrie",
	"StoragesTrie",
	"HashedAccounts",
	"HashedStorages",
}

func cfg(d kv.TableCfg) kv.TableCfg {
	for _, t := range knownTables {
		d[t] = kv.TableCfgItem{}
	}
	d["AccountChangeSets"] = kv.TableCfgItem{Flags: kv.DupSort}
	d["StorageChangeSets"] = kv.TableCfgItem{Flags: kv.DupSort}
	d["HashedStorages"] = kv.TableCfgItem{Flags: kv.DupSort}
	d["StoragesTrie"] = kv.TableCfgItem{Flags: kv.DupSort}
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

	sort.Strings(knownTables)
	for _, t := range knownTables {
		cur, err := tx.Cursor(t)
		if err != nil {
			fmt.Printf("%-25s NOT FOUND (%v)\n", t, err)
			continue
		}
		n, err := cur.Count()
		cur.Close()
		if err != nil {
			fmt.Printf("%-25s count err: %v\n", t, err)
			continue
		}
		fmt.Printf("%-25s entries=%d\n", t, n)
	}
}
