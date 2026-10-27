// Command hotstuff-reset performs an emergency reset of a node's persisted
// HotStuff round state. Run it with the node STOPPED.
//
// By default it keeps a floor: the last committed QC survives and everything
// above it is lowered to it -- the lock (highQC), both vote commitments --
// and the view restarts at committedView+1. That clears a LockedQC naming a
// branch this node never applied (the wedge the node reports as "PERSISTED
// CONSENSUS STATE HAS DIVERGED") and a vote or lock on a speculative block
// the node's startup revert unwinds (round S132: votes at view 46713 for the
// uncommitted 14152208 made the fleet extend it after the restart). The
// committed QC is kept only when its block is canonical locally, or when it
// sits above the canonical head on the canonical head's own lineage (the
// node committed it but stopped before the block's lagged write was durable;
// the node canonicalizes it after the restart once the block is applied).
// Otherwise the tool refuses unless -force-wipe is given, which deletes the
// whole record as older versions did (views then restart at 1).
//
// Lowering the votes gives up the crash-time no-equivocation guard for the
// views between the committed QC and the old vote: run it on every
// validator of the fleet together, with every node stopped, never on one
// node of a live fleet.
//
// The separate vote journal (<node datadir>/hotstuff-journal, the parent of
// -datadir, or -journal) is cleared too: recovery merges it monotonically
// into the chain record and would raise the lowered lock and votes again
// (S132: three nodes came back locked at 46713 after a lock-only reset).
//
// Round S131 is why the floor exists: a full wipe on every datadir let nodes
// that had committed f343 vote, after the restart, for a sibling of it.
//
// -print is read-only and prints one line for a runner's pre-reset check:
//
//	datadir=<dir> committed_height=<n> committed_hash=<0x..> canonical_height=<n> canonical_hash=<0x..> hs_committed_qc_view=<v> hs_committed_qc_hash=<0x..>
//
// committed_* is the QC-backed HotStuffCommittedHead marker (what the vote
// floor reads), canonical_* the head block pointer, hs_committed_qc_* the
// persisted HotStuff LastCommittedQC; an unknown height prints as "-".
//
// The chain data and epoch records are untouched. Applying requires an
// exclusive, fsynced byte-for-byte backup.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
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

// chainPoint is a block height and hash; known is false when the height
// could not be resolved.
type chainPoint struct {
	number uint64
	hash   types.Hash
	known  bool
}

func (p chainPoint) height() string {
	if !p.known {
		return "-"
	}
	return fmt.Sprintf("%d", p.number)
}

func readPoint(tx kv.Tx, hash types.Hash) chainPoint {
	p := chainPoint{hash: hash}
	if hash == (types.Hash{}) {
		return p
	}
	if n := rawdb.ReadHeaderNumber(tx, hash); n != nil {
		p.number, p.known = *n, true
	}
	return p
}

// printLine formats the -print output (see the package comment).
func printLine(dataDir string, committed, canonical chainPoint, state *hotstuff.ConsensusState) string {
	var qcView uint64
	var qcHash types.Hash
	if state != nil {
		qcView, qcHash = uint64(state.LastCommittedQC.View), state.LastCommittedQC.BlockHash
	}
	return fmt.Sprintf("datadir=%s committed_height=%s committed_hash=%s canonical_height=%s canonical_hash=%s hs_committed_qc_view=%d hs_committed_qc_hash=%s",
		dataDir, committed.height(), committed.hash.Hex(), canonical.height(), canonical.hash.Hex(), qcView, qcHash.Hex())
}

// floorState is the record the default reset writes: the committed QC kept,
// the lock and both vote commitments lowered to it, the view at
// committedView+1, the timeout backoff cleared.
func floorState(state *hotstuff.ConsensusState) *hotstuff.ConsensusState {
	qc := state.LastCommittedQC
	return &hotstuff.ConsensusState{
		View:                qc.View + 1,
		LockedQC:            qc.Clone(),
		LastCommittedQC:     qc.Clone(),
		LastVotedView:       qc.View,
		LastVotedHash:       qc.BlockHash,
		LastCommitVotedView: qc.View,
		LastCommitVotedHash: qc.BlockHash,
	}
}

// committedQCCanonical reports whether the persisted committed QC names a
// block on this node's canonical chain (a genesis QC trivially does).
func committedQCCanonical(tx kv.Tx, state *hotstuff.ConsensusState) (bool, error) {
	h := state.LastCommittedQC.BlockHash
	if h == (types.Hash{}) {
		return true, nil
	}
	n := rawdb.ReadHeaderNumber(tx, h)
	if n == nil {
		return false, nil
	}
	ch, err := rawdb.ReadCanonicalHash(tx, *n)
	if err != nil {
		return false, err
	}
	return ch == h, nil
}

// applyReset rewrites (or, with wipe, deletes) the persisted state in one
// transaction. Deleting first keeps SaveConsensusState's monotonic merge from
// folding the old lock back in.
func applyReset(ctx context.Context, db kv.RwDB, state *hotstuff.ConsensusState, wipe bool) error {
	return db.Update(ctx, func(tx kv.RwTx) error {
		if err := tx.Delete(modules.HotStuffState, stateKey); err != nil {
			return err
		}
		if wipe {
			return nil
		}
		return hotstuff.SaveConsensusState(tx, floorState(state))
	})
}

// defaultJournalPath is the separate vote journal's store for a chaindata
// directory: <node datadir>/hotstuff-journal, the node datadir being the
// parent of chaindata.
func defaultJournalPath(chaindata string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(chaindata)), "hotstuff-journal")
}

// readJournal returns the journal store's raw record (nil when absent).
func readJournal(ctx context.Context, db kv.RoDB) ([]byte, error) {
	var raw []byte
	err := db.View(ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne(modules.HotStuffState, stateKey)
		raw = append(raw, v...)
		return err
	})
	return raw, err
}

// clearJournal deletes the journal store's record: recovery then takes the
// chain database's record as it is (mergeVoteJournal).
func clearJournal(ctx context.Context, db kv.RwDB) error {
	return db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Delete(modules.HotStuffState, stateKey)
	})
}

func openJournal(ctx context.Context, path string) (kv.RwDB, error) {
	return mdbx.NewMDBX(log.New()).Path(path).Label(kv.ConsensusDB).
		WithTableCfg(hotstuff.VoteJournalTables).Accede().Open(ctx)
}

// readDatadir reads everything both modes need in one read transaction.
//
// qcAhead reports a committed QC whose block descends from, and is above, the
// canonical head (hotstuff.CommittedQCAheadOfCanonical). Its floor is kept as
// it is: the QC is genuine, and lowering the floor to the canonical head would
// need a QC for that block, which the record does not hold.
func readDatadir(ctx context.Context, db kv.RoDB) (raw []byte, state *hotstuff.ConsensusState, committed, canonical chainPoint, qcCanonical, qcAhead bool, err error) {
	err = db.View(ctx, func(tx kv.Tx) error {
		committed = readPoint(tx, rawdb.ReadHotStuffCommittedHead(tx))
		canonical = readPoint(tx, rawdb.ReadHeadBlockHash(tx))
		v, err := tx.GetOne(modules.HotStuffState, stateKey)
		if err != nil {
			return err
		}
		raw = append(raw[:0], v...)
		if len(raw) == 0 {
			return nil
		}
		if state, err = hotstuff.LoadConsensusState(tx); err != nil || state == nil {
			return err
		}
		if qcCanonical, err = committedQCCanonical(tx, state); err != nil || qcCanonical {
			return err
		}
		qcAhead, _, err = hotstuff.CommittedQCAheadOfCanonical(tx, state.LastCommittedQC.BlockHash)
		return err
	})
	return raw, state, committed, canonical, qcCanonical, qcAhead, err
}

func main() {
	dataDir := flag.String("datadir", "", "node data dir (the one holding chaindata)")
	apply := flag.Bool("apply", false, "reset the state after all safety checks")
	force := flag.Bool("force", false, "acknowledge the lowered lock")
	forceWipe := flag.Bool("force-wipe", false, "delete the whole record, losing the committed floor and the crash-equivocation guard")
	printOnly := flag.Bool("print", false, "read-only: print the committed and canonical heads as one line")
	backup := flag.String("backup", "", "new file for the exact state bytes (required with -apply)")
	journal := flag.String("journal", "", "separate vote journal store (default <parent of -datadir>/hotstuff-journal, skipped when absent)")
	flag.Parse()
	if *dataDir == "" {
		fmt.Fprintln(os.Stderr, "-datadir is required")
		os.Exit(2)
	}
	if *printOnly && *apply {
		fmt.Fprintln(os.Stderr, "-print is read-only and cannot be combined with -apply")
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

	raw, state, committed, canonical, qcCanonical, qcAhead, err := readDatadir(ctx, db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read/decode: %v\n", err)
		os.Exit(1)
	}
	if *printOnly {
		fmt.Println(printLine(*dataDir, committed, canonical, state))
		return
	}
	if len(raw) == 0 {
		fmt.Printf("%s: no persisted hotstuff state\n", *dataDir)
		return
	}
	fmt.Printf("%s: hotstuff state present (%d bytes)\n", *dataDir, len(raw))
	if state != nil {
		fmt.Printf("view=%d lockedQC=%d committedQC=%d prepareVote=%d commitVote=%d committedQCCanonical=%v committedQCAheadOfCanonical=%v\n",
			state.View, state.LockedQC.View, state.LastCommittedQC.View,
			state.LastVotedView, state.LastCommitVotedView, qcCanonical, qcAhead)
	}
	wipe := *forceWipe
	if !wipe && (state == nil || !(qcCanonical || qcAhead)) {
		fmt.Fprintln(os.Stderr, "the persisted committed QC is not on this node's canonical chain (or the record does not decode); "+
			"no floor can be kept. Re-run with -force-wipe to delete the whole record.")
		os.Exit(1)
	}
	if !*apply {
		if wipe {
			fmt.Println("dry-run only; -force-wipe would delete the whole record")
		} else {
			f := floorState(state)
			fmt.Printf("dry-run only; would keep committedQC=%d (%s), lowering lockedQC %d -> %d, prepareVote %d -> %d, commitVote %d -> %d, view %d -> %d\n",
				state.LastCommittedQC.View, state.LastCommittedQC.BlockHash.Hex(),
				state.LockedQC.View, f.LockedQC.View, state.LastVotedView, f.LastVotedView,
				state.LastCommitVotedView, f.LastCommitVotedView, state.View, f.View)
		}
		fmt.Println("emergency apply requires -apply -force -backup <new-file>")
		return
	}
	// The journal first: a reset whose journal still holds the old votes is
	// undone at the next start.
	jpath := *journal
	if jpath == "" {
		jpath = defaultJournalPath(*dataDir)
	}
	var jdb kv.RwDB
	if _, statErr := os.Stat(jpath); statErr == nil {
		if jdb, err = openJournal(ctx, jpath); err != nil {
			fmt.Fprintf(os.Stderr, "open vote journal %s: %v\n", jpath, err)
			os.Exit(1)
		}
		defer jdb.Close()
	} else if *journal != "" {
		fmt.Fprintf(os.Stderr, "vote journal %s: %v\n", jpath, statErr)
		os.Exit(1)
	}
	if err := writeExclusiveBackup(*backup, raw); err != nil {
		fmt.Fprintf(os.Stderr, "backup: %v\n", err)
		os.Exit(1)
	}
	if jdb != nil {
		jraw, err := readJournal(ctx, jdb)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read vote journal: %v\n", err)
			os.Exit(1)
		}
		if len(jraw) > 0 {
			if err := writeExclusiveBackup(*backup+".journal", jraw); err != nil {
				fmt.Fprintf(os.Stderr, "journal backup: %v\n", err)
				os.Exit(1)
			}
			if err := clearJournal(ctx, jdb); err != nil {
				fmt.Fprintf(os.Stderr, "clear vote journal (backup retained at %s.journal): %v\n", *backup, err)
				os.Exit(1)
			}
			fmt.Printf("%s: cleared the vote journal record; exact backup saved at %s.journal\n", jpath, *backup)
		}
	}
	if err := applyReset(ctx, db, state, wipe); err != nil {
		fmt.Fprintf(os.Stderr, "reset (backup retained at %s): %v\n", *backup, err)
		os.Exit(1)
	}
	what := "lowered the lock, both votes and the view to the committed QC (committed QC kept)"
	if wipe {
		what = "deleted the whole hotstuff record"
	}
	if _, err := io.WriteString(os.Stdout,
		fmt.Sprintf("%s: %s; exact backup saved at %s\n", *dataDir, what, *backup)); err != nil {
		fmt.Fprintf(os.Stderr, "report result: %v\n", err)
	}
}
