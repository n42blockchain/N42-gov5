// Quick reporter for chaindata table entry counts using cursor walk.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func count(tx kv.Tx, t string) (uint64, error) {
	c, err := tx.Cursor(t)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	var n uint64
	for k, _, err := c.First(); k != nil; k, _, err = c.Next() {
		if err != nil {
			return n, err
		}
		n++
		if n%5_000_000 == 0 {
			fmt.Fprintf(os.Stderr, "  %s: %d ...\n", t, n)
		}
	}
	return n, nil
}

func main() {
	dir := flag.String("dir", `D:/N42-eth1177-test/chaindata`, "chaindata path")
	tables := flag.String("tables", "CommitmentBranches,Code,SyncStage", "comma list")
	flag.Parse()

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()

	for _, t := range strings.Split(*tables, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		n, err := count(tx, t)
		if err != nil {
			fmt.Printf("%-22s  entries=%d err=%v\n", t, n, err)
			continue
		}
		fmt.Printf("%-22s  entries=%d\n", t, n)
	}
}
