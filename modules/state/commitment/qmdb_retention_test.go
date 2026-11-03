package commitment

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
)

type retentionReads struct {
	qmdb.Getter
	entries int
}

func (r *retentionReads) GetOne(table string, key []byte) ([]byte, error) {
	if table == qmdb.EntryTable {
		r.entries++
	}
	return r.Getter.GetOne(table, key)
}

func TestQMDBRetentionPersistenceUndoAndReload(t *testing.T) {
	previous := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = previous })
	coldDB, hotDB := memdb.NewTestDB(t), memdb.NewTestDB(t)
	cold, hot := NewQMDBRootComputer(), NewQMDBRootComputer()
	cold.retainedEntries = 0
	hot.retainedEntries = 4096
	cold.EnableUndoRecording()
	hot.EnableUndoRecording()
	roots := []types.Hash{cold.Root()}
	var undos []*qmdb.BlockUndo
	expected := map[types.Address]*account.StateAccount{}
	apply := func(db kv.RwDB, rc *QMDBRootComputer, changes map[types.Address]*account.StateAccount) (*qmdb.BlockUndo, types.Hash, int) {
		tx, err := db.BeginRw(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		count := &retentionReads{Getter: tx}
		rc.SetCold(count)
		root, err := rc.ComputeRoot(changes, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rc.FlushTo(tx); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		rc.CommitFlushed()
		rc.EvictFlushed()
		rc.SetCold(nil)
		return rc.TakeUndo(), root, count.entries
	}
	checkValues := func(db kv.RwDB, rc *QMDBRootComputer) {
		tx, err := db.BeginRo(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		rc.SetCold(tx)
		defer rc.SetCold(nil)
		for i := 0; i < 2400; i++ {
			addr := rlAddr(i)
			key := qmdb.Hash(AccountKeyHash(addr))
			got, found, err := rc.Tree().GetChecked(key)
			if err != nil {
				t.Fatal(err)
			}
			want := expected[addr]
			if want == nil {
				if found {
					t.Fatalf("deleted/missing account %d returned", i)
				}
				continue
			}
			encoded := make([]byte, want.EncodingLengthForStorage())
			want.EncodeForStorage(encoded)
			if !found || !bytes.Equal(got, encoded) {
				t.Fatalf("account %d differs", i)
			}
			if i%199 == 0 {
				p, ok := rc.Tree().GetProof(key)
				if !ok || !qmdb.VerifyProof(qmdb.Hash(rc.Root()), p) {
					t.Fatalf("invalid proof for %d", i)
				}
			}
		}
	}
	for height := 1; height <= 10; height++ {
		changes := make(map[types.Address]*account.StateAccount)
		for i := 0; i < 1200; i++ {
			addr := rlAddr((height%2)*1200 + i)
			var a *account.StateAccount
			if (height+i)%17 != 0 {
				a = qmAcct(uint64(height), uint64(i+100))
			}
			changes[addr] = a
			expected[addr] = a
		}
		cu, cr, cReads := apply(coldDB, cold, changes)
		hu, hr, hReads := apply(hotDB, hot, changes)
		if cr != hr || !bytes.Equal(cu.Marshal(), hu.Marshal()) {
			t.Fatalf("root/undo differ at %d", height)
		}
		if hot.Tree().ResidentEntries() > 4096 {
			t.Fatal("retained entry window exceeded")
		}
		if hot.ResidentTwigLeaves() > 4 {
			t.Fatal("sealed twig window exceeded")
		}
		if cold.Tree().ResidentEntries() != 0 {
			t.Fatal("zero retention changed")
		}
		if height >= 4 && (cReads == 0 || hReads >= cReads) {
			t.Fatalf("retention did not reduce entry reads: cold=%d hot=%d", cReads, hReads)
		}
		roots = append(roots, cr)
		undos = append(undos, hu)
		for _, table := range []string{qmdb.EntryTable, qmdb.TwigTable, qmdb.MetaTable, qmdb.LeavesTable} {
			if !reflect.DeepEqual(dumpTbl(t, coldDB, table), dumpTbl(t, hotDB, table)) {
				t.Fatalf("persisted %s differs at %d", table, height)
			}
		}
		checkValues(coldDB, cold)
		checkValues(hotDB, hot)
	}
	// Every snapshot can be reconstructed entirely from disk despite retaining
	// its hot values. No state work was postponed into the memory window.
	tx, err := hotDB.BeginRo(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	fresh := NewQMDBRootComputer()
	fresh.SetCold(tx)
	if err := fresh.LoadFrom(tx); err != nil {
		t.Fatal(err)
	}
	if fresh.Root() != hot.Root() {
		t.Fatal("restart root differs")
	}
	fresh.SetCold(nil)
	tx.Rollback()
	// Rewind farther than the hot window, then apply an alternative branch.
	for i := len(undos) - 1; i >= 0; i-- {
		for _, pair := range []struct {
			db kv.RwDB
			rc *QMDBRootComputer
		}{{coldDB, cold}, {hotDB, hot}} {
			if err := pair.db.Update(t.Context(), func(tx kv.RwTx) error { return pair.rc.RevertBlock(tx, undos[i]) }); err != nil {
				t.Fatal(err)
			}
			pair.rc.EvictFlushed()
			pair.rc.SetCold(nil)
			if pair.rc.Root() != roots[i] {
				t.Fatalf("undo root differs at %d", i)
			}
		}
	}
	changes := map[types.Address]*account.StateAccount{rlAddr(42): qmAcct(1, 777)}
	_, cr, _ := apply(coldDB, cold, changes)
	_, hr, _ := apply(hotDB, hot, changes)
	if cr != hr {
		t.Fatal("alternative branch root differs")
	}
}

func TestParseQMDBRetainedEntries(t *testing.T) {
	for value, want := range map[string]uint64{"": 0, "bad": 0, "-1": 0, "0": 0, "4096": 4096, "999999999": maxQMDBRetainedEntries, "18446744073709551616": 0} {
		if got := parseQMDBRetainedEntries(value); got != want {
			t.Fatalf("%q: %d != %d", value, got, want)
		}
	}
}
