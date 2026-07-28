// Command txpool-journal-reset clears a node's persisted transaction-pool journal.
//
// This tool was written to unwedge a startup deadlock: NewTxsPool restored the
// journal before its background loops existed, so the restore blocked forever on
// a channel nobody was reading and any node with a non-empty journal never
// finished starting. That ordering is fixed (the restore now runs after the
// scheduler loop, and a guard downgrades a re-introduction to a logged skip), so
// clearing the journal is no longer needed to boot a node.
//
// It is kept as an operational escape hatch for the cases where you want the
// queued transactions gone rather than replayed: a journal full of entries that
// are wasteful to re-validate, a pool poisoned by a bad load-test run, or a node
// being handed to a different chain state. Inspect first — hotstuff-inspect
// reports the journal size alongside the consensus record — and note that
// without -apply this only counts.
//
// Only queued transactions are lost; they were never part of the chain. Run it
// with the node stopped.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func main() {
	dataDir := flag.String("datadir", "", "node chaindata dir")
	apply := flag.Bool("apply", false, "actually clear; without it the journal is only counted")
	flag.Parse()
	if *dataDir == "" {
		fmt.Fprintln(os.Stderr, "-datadir is required")
		os.Exit(2)
	}

	ctx := context.Background()
	logger := log.New()
	db, err := mdbx.NewMDBX(logger).Path(*dataDir).Accede().Open(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", *dataDir, err)
		os.Exit(1)
	}
	defer db.Close()

	var n int
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		exists, e := tx.ExistsBucket(modules.TxPoolJournal)
		if e != nil || !exists {
			return e
		}
		c, e := tx.Cursor(modules.TxPoolJournal)
		if e != nil {
			return e
		}
		defer c.Close()
		for k, _, e := c.First(); k != nil; k, _, e = c.Next() {
			if e != nil {
				return e
			}
			n++
		}
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "read: %v\n", err)
		os.Exit(1)
	}

	if n == 0 {
		fmt.Printf("%s: journal already empty\n", *dataDir)
		return
	}
	if !*apply {
		fmt.Printf("%s: %d journalled txs — rerun with -apply to clear\n", *dataDir, n)
		return
	}
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return tx.ClearBucket(modules.TxPoolJournal)
	}); err != nil {
		fmt.Fprintf(os.Stderr, "clear: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s: cleared %d journalled txs\n", *dataDir, n)
}
