package node

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// The pruner persists its horizon before deleting, and historical state
// below it is refused afterwards (and after a restart).
func TestPruner_PersistsHorizonAndRefusesBelow(t *testing.T) {
	db := memdb.NewTestDB(t)
	t.Cleanup(rawdb.ResetStatePrunedBelowForTesting)
	pruner := NewPruner(db, conf.PruneConfig{Mode: conf.PruneModeFull, BlockRetention: 50, PruneInterval: 10, PruneBatchLimit: 100},
		&staticBlockProvider{block: 200}, nil)
	pruner.maybePrune()

	if err := db.View(testCtx(), func(tx kv.Tx) error {
		pb, ok, err := rawdb.ReadStatePrunedBelow(tx)
		if err != nil || !ok || pb != 150 {
			t.Fatalf("persisted horizon = %d %v %v, want 150", pb, ok, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := rawdb.CheckHistoricalState(149); !errors.Is(err, rawdb.ErrHistoricalStatePruned) {
		t.Fatalf("block 149: err = %v, want ErrHistoricalStatePruned", err)
	}
	if err := rawdb.CheckHistoricalState(150); err != nil {
		t.Fatalf("block 150: %v", err)
	}
	// The horizon never moves down.
	if err := db.Update(testCtx(), func(tx kv.RwTx) error { return rawdb.RaiseStatePrunedBelow(tx, 10) }); err != nil {
		t.Fatal(err)
	}
	if got := rawdb.StatePrunedBelow(); got != 150 {
		t.Fatalf("horizon lowered to %d", got)
	}
}

// pruneHistoryTableFrom resumes where the previous bounded pass stopped
// instead of rescanning kept rows from the first key, and drains everything
// below the horizon across passes.
func TestPruneHistoryTableResumes(t *testing.T) {
	db := memdb.NewTestDB(t)
	key := func(addr byte, shard uint64) []byte {
		k := make([]byte, 21+8)
		k[0] = addr
		binary.BigEndian.PutUint64(k[21:], shard)
		return k
	}
	if err := db.Update(testCtx(), func(tx kv.RwTx) error {
		for a := byte(0); a < 30; a++ {
			for _, shard := range []uint64{10, 20, ^uint64(0)} {
				if err := tx.Put(modules.AccountsHistory, key(a, shard), []byte{1}); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var resume []byte
	passes := 0
	for {
		if err := db.Update(testCtx(), func(tx kv.RwTx) error {
			var err error
			resume, err = pruneHistoryTableFrom(tx, modules.AccountsHistory, resume, 25, 7, testCtx())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		passes++
		if resume == nil || passes > 20 {
			break
		}
	}
	if passes != 9 { // 60 rows to delete, 7 per pass
		t.Fatalf("passes = %d, want 9", passes)
	}
	if err := db.View(testCtx(), func(tx kv.Tx) error {
		n := 0
		return tx.ForEach(modules.AccountsHistory, nil, func(k, _ []byte) error {
			if binary.BigEndian.Uint64(k[len(k)-8:]) < 25 {
				t.Fatalf("row below the horizon survived: %x", k)
			}
			n++
			if n > 30 {
				t.Fatalf("too many rows kept")
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
}

// failHistoryCommitDB rolls back (and fails) any Update that changed the
// AccountsHistory table, simulating a commit failure of the history pass.
type failHistoryCommitDB struct {
	kv.RwDB
	fail bool
}

var errInjectedCommit = errors.New("injected commit failure")

func countRows(tx kv.Tx, table string) (int, error) {
	n := 0
	err := tx.ForEach(table, nil, func(_, _ []byte) error { n++; return nil })
	return n, err
}

func (d *failHistoryCommitDB) Update(ctx context.Context, f func(tx kv.RwTx) error) error {
	tx, err := d.RwDB.BeginRw(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	before, _ := countRows(tx, modules.AccountsHistory)
	if err := f(tx); err != nil {
		return err
	}
	after, _ := countRows(tx, modules.AccountsHistory)
	if d.fail && after != before {
		return errInjectedCommit // deferred Rollback discards the pass
	}
	return tx.Commit()
}

// The history resume point only advances when the pass committed.
func TestPrunerHistoryResumeAdvancesOnlyOnCommit(t *testing.T) {
	inner := memdb.NewTestDB(t)
	t.Cleanup(rawdb.ResetStatePrunedBelowForTesting)
	db := &failHistoryCommitDB{RwDB: inner, fail: true}
	key := func(addr byte, shard uint64) []byte {
		k := make([]byte, 21+8)
		k[0] = addr
		binary.BigEndian.PutUint64(k[21:], shard)
		return k
	}
	if err := inner.Update(testCtx(), func(tx kv.RwTx) error {
		for a := byte(0); a < 30; a++ {
			if err := tx.Put(modules.AccountsHistory, key(a, 10), []byte{1}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	pruner := NewPruner(db, conf.PruneConfig{Mode: conf.PruneModeFull, BlockRetention: 50, PruneInterval: 10, PruneBatchLimit: 7},
		&staticBlockProvider{block: 200}, nil)
	if err := pruner.prune(150); !errors.Is(err, errInjectedCommit) {
		t.Fatalf("prune err = %v, want the injected failure", err)
	}
	if len(pruner.historyResume[modules.AccountsHistory]) != 0 {
		t.Fatalf("resume advanced over a rolled-back pass: %x", pruner.historyResume[modules.AccountsHistory])
	}
	db.fail = false
	for i := 0; i < 10; i++ {
		if err := pruner.prune(150); err != nil {
			t.Fatal(err)
		}
	}
	if err := inner.View(testCtx(), func(tx kv.Tx) error {
		n, err := countRows(tx, modules.AccountsHistory)
		if err != nil || n != 0 {
			t.Fatalf("rows left after retries: %d (%v)", n, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
