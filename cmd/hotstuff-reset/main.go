// Command hotstuff-reset clears a node's persisted HotStuff consensus state.
//
// The persisted state pins LockedQC/LastCommittedQC. If a restart resumes with a
// LockedQC naming a block the node never applied — which is reachable today,
// because persistState runs only every few views and does not record the vote
// state at all — then AlignAppliedBranch refuses to move the applied branch onto
// that parent ("would revert committed block") and the node stops producing
// forever. Dropping the key makes consensus restart from the applied head.
//
// The chain data itself is untouched: only the consensus lock state is removed.
// Run it with the node STOPPED.
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
	dataDir := flag.String("datadir", "", "node data dir (the one holding chaindata)")
	apply := flag.Bool("apply", false, "actually delete; without it the key is only reported")
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

	var present bool
	var size int
	if err := db.View(ctx, func(tx kv.Tx) error {
		v, e := tx.GetOne(modules.HotStuffState, []byte("state"))
		present, size = len(v) > 0, len(v)
		return e
	}); err != nil {
		fmt.Fprintf(os.Stderr, "read: %v\n", err)
		os.Exit(1)
	}

	if !present {
		fmt.Printf("%s: no persisted hotstuff state\n", *dataDir)
		return
	}
	if !*apply {
		fmt.Printf("%s: hotstuff state present (%d bytes) — rerun with -apply to clear\n", *dataDir, size)
		return
	}
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Delete(modules.HotStuffState, []byte("state"))
	}); err != nil {
		fmt.Fprintf(os.Stderr, "delete: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s: cleared hotstuff state (%d bytes)\n", *dataDir, size)
}
