// One-shot tool to ClearBucket a named table in a chaindata MDBX env.
// Used to wipe partial / inconsistent CommitmentBranches before a fresh
// bulk rebuild.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func runClear(args []string) {
	fs := flag.NewFlagSet("n42-table clear", flag.ExitOnError)
	dir := fs.String("dir", `D:/N42-eth1177-test/chaindata`, "chaindata MDBX path")
	table := fs.String("table", "CommitmentBranches", "table name to clear")
	fs.Parse(args)

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Accede().
		Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		fmt.Printf("clearing table %q in %s\n", *table, *dir)
		return tx.ClearBucket(*table)
	}); err != nil {
		fmt.Fprintln(os.Stderr, "clear:", err)
		os.Exit(1)
	}
	fmt.Println("done")
}
