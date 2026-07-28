// Command hotstuff-inspect prints the persisted HotStuff consensus record of one
// or more STOPPED nodes, read-only.
//
// It is the read-only counterpart to cmd/hotstuff-reset: that tool deletes the
// record to unwedge a node, this one lets you see what you would be deleting —
// and, more usefully, lets you capture the durable vote commitments across a
// restart. The vote fields (lastVoted / lastCommitVoted) are what HotStuff
// safety rests on: they must be at the chain tip before a stop and must come
// back identical after the restart. A record that shows them at zero, or behind
// the locked QC, means a write regressed them and the node can equivocate on
// its next start.
//
// Given several data dirs it also reports whether they agree, which is the
// normal question when inspecting a validator fleet.
//
// Run with the nodes stopped. The database is opened read-only, so a running
// node is not disturbed, but its record is a snapshot of the last persist
// rather than the engine's live state.
//
// Usage:
//
//	hotstuff-inspect E:\qs-node0 E:\qs-node1 ...
//	hotstuff-inspect -datadir E:\qs-node0
//
// A path may point either at the data dir or at the chaindata dir inside it.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

type record struct {
	dir     string
	state   *hotstuff.ConsensusState
	journal int
}

func main() {
	dataDir := flag.String("datadir", "", "node data dir (or its chaindata dir); may also be given positionally")
	flag.Parse()

	dirs := flag.Args()
	if *dataDir != "" {
		dirs = append([]string{*dataDir}, dirs...)
	}
	if len(dirs) == 0 {
		fmt.Fprintln(os.Stderr, "usage: hotstuff-inspect <datadir> [datadir...]")
		os.Exit(2)
	}

	records := make([]record, 0, len(dirs))
	failed := false
	for _, d := range dirs {
		r, err := inspect(d)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", d, err)
			failed = true
			continue
		}
		records = append(records, r)
		printRecord(r)
	}

	if len(records) > 1 {
		printAgreement(records)
	}
	if failed {
		os.Exit(1)
	}
}

// resolveDB accepts either the node data dir or the chaindata dir inside it.
func resolveDB(dir string) string {
	if fi, err := os.Stat(filepath.Join(dir, "chaindata")); err == nil && fi.IsDir() {
		return filepath.Join(dir, "chaindata")
	}
	return dir
}

func inspect(dir string) (record, error) {
	ctx := context.Background()
	db, err := mdbx.NewMDBX(log.New()).Path(resolveDB(dir)).Accede().Readonly().Open(ctx)
	if err != nil {
		return record{}, fmt.Errorf("open: %w", err)
	}
	defer db.Close()

	r := record{dir: dir}
	err = db.View(ctx, func(tx kv.Tx) error {
		st, e := hotstuff.LoadConsensusState(tx)
		if e != nil {
			return e
		}
		r.state = st
		// Journal size is reported alongside because a non-empty journal is what
		// the restore path has to survive on the next start.
		c, e := tx.Cursor(modules.TxPoolJournal)
		if e != nil {
			return nil // table absent on this node
		}
		defer c.Close()
		for k, _, e := c.First(); k != nil; k, _, e = c.Next() {
			if e != nil {
				return e
			}
			r.journal++
		}
		return nil
	})
	if err != nil {
		return record{}, fmt.Errorf("read: %w", err)
	}
	return r, nil
}

func printRecord(r record) {
	if r.state == nil {
		fmt.Printf("%s: no persisted consensus state (journal=%d)\n", r.dir, r.journal)
		return
	}
	s := r.state
	fmt.Printf("%s\n", r.dir)
	fmt.Printf("  view=%d timeouts=%d journal=%d\n", s.View, s.ConsecutiveTimeouts, r.journal)
	fmt.Printf("  lockedQC=%d/%x  committedQC=%d/%x\n",
		s.LockedQC.View, s.LockedQC.BlockHash[:6], s.LastCommittedQC.View, s.LastCommittedQC.BlockHash[:6])
	fmt.Printf("  lastVoted=%s  lastCommitVoted=%s\n",
		voteString(s.LastVotedView, s.LastVotedHash[:6]),
		voteString(s.LastCommitVotedView, s.LastCommitVotedHash[:6]))
	// A vote behind the lock is the shape a regressed write leaves behind: the
	// node would consider itself free to vote again in a view it already
	// committed to.
	if s.LastVotedView != 0 && s.LastVotedView < s.LockedQC.View {
		fmt.Printf("  WARNING: lastVoted (%d) is behind lockedQC (%d) — the vote record may have regressed\n",
			s.LastVotedView, s.LockedQC.View)
	}
}

func voteString(view hotstuff.ViewNumber, hash []byte) string {
	if view == 0 {
		return "never"
	}
	return fmt.Sprintf("%d/%x", view, hash)
}

// printAgreement summarises whether a set of nodes hold the same record, which
// is the question worth asking of a validator fleet.
func printAgreement(rs []record) {
	views := map[hotstuff.ViewNumber]int{}
	votes := map[string]int{}
	for _, r := range rs {
		if r.state == nil {
			views[0]++
			votes["<none>"]++
			continue
		}
		views[r.state.View]++
		votes[fmt.Sprintf("%d/%x", r.state.LastVotedView, r.state.LastVotedHash)]++
	}
	fmt.Printf("\n%d nodes: %d distinct view(s), %d distinct lastVoted record(s)\n",
		len(rs), len(views), len(votes))
	if len(views) > 1 || len(votes) > 1 {
		fmt.Println("  nodes disagree — expected during live operation, but after a clean fleet-wide stop they should match")
	}
}
