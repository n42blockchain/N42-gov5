package node

import (
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
