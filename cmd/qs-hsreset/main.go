// Command qs-hsreset deletes the persisted HotStuff engine state
// (modules.HotStuffState) from one or more STOPPED validator chaindata dirs.
//
// Surgery tool for a poisoned consensus checkpoint: once an engine's
// persisted LockedQC/CommitQC points at a block whose state the network can
// never execute (e.g. a forked leader's block certified before the
// applied-evidence vote gate existed), the HotStuff safety rules — correctly —
// forbid ever abandoning it, so the network cannot converge again by protocol
// means. Wiping the persisted engine state on EVERY validator (all stopped,
// then all restarted together) cold-starts the round state from the canonical
// chain, which the commit-authority-only writer kept clean.
//
//	qs-hsreset E:/qs-node0/chaindata E:/qs-node1/chaindata ...
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func main() {
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: qs-hsreset <chaindata dir>...")
		os.Exit(1)
	}
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	failed := false
	for _, dir := range flag.Args() {
		if err := resetOne(dir); err != nil {
			fmt.Printf("%s: %v\n", dir, err)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}

func resetOne(dir string) error {
	db, err := mdbxkv.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).
		MapSize(64 * datasize.GB).Accede().Open(context.Background())
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer db.Close()
	return db.Update(context.Background(), func(tx kv.RwTx) error {
		c, err := tx.RwCursor(modules.HotStuffState)
		if err != nil {
			return err
		}
		defer c.Close()
		n := 0
		for {
			k, _, err := c.First()
			if err != nil {
				return err
			}
			if k == nil {
				break
			}
			if err := c.DeleteCurrent(); err != nil {
				return err
			}
			n++
		}
		fmt.Printf("%s: deleted %d HotStuffState keys\n", dir, n)
		return nil
	})
}
