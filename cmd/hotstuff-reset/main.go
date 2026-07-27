// Command hotstuff-reset performs an emergency removal of a node's persisted
// HotStuff round/vote journal. Deleting this record removes the crash-time
// no-equivocation guard required by SR9, so it is only safe during a coordinated
// stop after an operator has verified the applied chain and consensus state.
//
// The chain data and epoch records are untouched. Applying requires an
// exclusive, fsynced byte-for-byte backup. Run it with the node STOPPED.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

var stateKey = []byte("state")

func validateApplyFlags(apply, force bool, backup string) error {
	if !apply {
		return nil
	}
	if !force {
		return errors.New("-apply requires -force")
	}
	if backup == "" {
		return errors.New("-apply requires -backup <new-file>")
	}
	return nil
}

func writeExclusiveBackup(path string, data []byte) (retErr error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err := f.Close(); retErr == nil && err != nil {
			retErr = err
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return nil
}

func main() {
	dataDir := flag.String("datadir", "", "node data dir (the one holding chaindata)")
	apply := flag.Bool("apply", false, "delete the journal after all safety checks")
	force := flag.Bool("force", false, "acknowledge loss of the crash-equivocation guard")
	backup := flag.String("backup", "", "new file for the exact journal bytes (required with -apply)")
	flag.Parse()
	if *dataDir == "" {
		fmt.Fprintln(os.Stderr, "-datadir is required")
		os.Exit(2)
	}
	if err := validateApplyFlags(*apply, *force, *backup); err != nil {
		fmt.Fprintln(os.Stderr, err)
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

	var (
		raw   []byte
		state *hotstuff.ConsensusState
	)
	if err := db.View(ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne(modules.HotStuffState, stateKey)
		if err != nil {
			return err
		}
		raw = append(raw[:0], v...)
		if len(raw) == 0 {
			return nil
		}
		state, err = hotstuff.LoadConsensusState(tx)
		return err
	}); err != nil {
		fmt.Fprintf(os.Stderr, "read/decode: %v\n", err)
		os.Exit(1)
	}

	if len(raw) == 0 {
		fmt.Printf("%s: no persisted hotstuff state\n", *dataDir)
		return
	}
	fmt.Printf("%s: hotstuff journal present (%d bytes)\n", *dataDir, len(raw))
	if state != nil {
		fmt.Printf("view=%d lockedQC=%d committedQC=%d prepareVote=%d commitVote=%d\n",
			state.View, state.LockedQC.View, state.LastCommittedQC.View,
			state.LastVotedView, state.LastCommitVotedView)
	}
	if !*apply {
		fmt.Println("dry-run only; emergency apply requires -apply -force -backup <new-file>")
		return
	}
	if err := writeExclusiveBackup(*backup, raw); err != nil {
		fmt.Fprintf(os.Stderr, "backup: %v\n", err)
		os.Exit(1)
	}
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Delete(modules.HotStuffState, stateKey)
	}); err != nil {
		fmt.Fprintf(os.Stderr, "delete (backup retained at %s): %v\n", *backup, err)
		os.Exit(1)
	}
	if _, err := io.WriteString(os.Stdout,
		fmt.Sprintf("%s: cleared hotstuff journal; exact backup saved at %s\n", *dataDir, *backup)); err != nil {
		fmt.Fprintf(os.Stderr, "report result: %v\n", err)
	}
}
