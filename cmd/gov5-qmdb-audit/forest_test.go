package main

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func auditTestDB(t *testing.T) kv.RwDB {
	t.Helper()
	modules.N42Init()
	db, err := mdbx.NewMDBX(log.New()).Path(t.TempDir()).Label(kv.ChainDB).
		WithTableCfg(func(kv.TableCfg) kv.TableCfg { return modules.N42TableCfg }).
		MapSize(256 * datasize.MB).GrowthStep(4 * datasize.MB).Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

// Recreate the old missing-blob failure without modifying any retained runtime:
// two sealed genesis twigs, a reclaimed genesis slot, and one active twig.
func TestAuditMissingGenesisBlobsAfterReclamation(t *testing.T) {
	db, genesis := auditTestDB(t), auditTestDB(t)
	var target types.Address
	var targetKey qmdb.Hash
	seed := func(tx kv.RwTx) error {
		for i := range 6000 {
			var addr types.Address
			binary.BigEndian.PutUint64(addr[12:], uint64(i+1))
			a := account.NewAccount()
			a.Balance.SetUint64(1000000)
			if err := tx.Put(modules.Account, addr[:], a.MarshalV2()); err != nil {
				return err
			}
			key := qmdb.Hash(commitment.AccountKeyHash(addr))
			if i == 0 || bytes.Compare(key[:], targetKey[:]) < 0 {
				target, targetKey = addr, key
			}
		}
		_, err := internal.SeedQMDBGenesis(tx)
		return err
	}
	for _, d := range []kv.RwDB{db, genesis} {
		if err := d.Update(t.Context(), seed); err != nil {
			t.Fatal(err)
		}
	}
	// Lose only the sealed genesis blobs, as on the old seed path. The active
	// twig will be rewritten by the account update and must never be overlaid.
	if err := db.Update(t.Context(), func(tx kv.RwTx) error {
		for id := range uint64(2) {
			if err := tx.Delete(qmdb.LeavesTable, position(id)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rc := commitment.NewQMDBRootComputer()
	if err := db.View(t.Context(), func(tx kv.Tx) error { rc.SetCold(tx); defer rc.SetCold(nil); return rc.LoadFrom(tx) }); err != nil {
		t.Fatal(err)
	}
	a := account.NewAccount()
	a.Nonce = 1
	a.Balance.SetUint64(999999)
	var root types.Hash
	if err := db.Update(t.Context(), func(tx kv.RwTx) error {
		rc.SetCold(tx)
		var err error
		root, err = rc.ComputeRoot(map[types.Address]*account.StateAccount{target: &a}, nil)
		if err != nil {
			return err
		}
		if err := tx.Put(modules.Account, target[:], a.MarshalV2()); err != nil {
			return err
		}
		_, err = rc.FlushTo(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rc.CommitFlushed()
	rc.EvictFlushed()
	rc.SetCold(nil)
	gtx, err := genesis.BeginRo(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer gtx.Rollback()
	audit := func() (forestReport, error) {
		var result forestReport
		err := db.View(t.Context(), func(tx kv.Tx) error { var err error; result, err = auditForest(tx, gtx, root); return err })
		return result, err
	}
	report, err := audit()
	if err != nil {
		t.Fatal(err)
	}
	if report.LiveKeys != 6000 || report.Root != root || len(report.MissingGenesis) != 2 {
		t.Fatalf("incomplete audit: %+v", report)
	}
	if err := db.View(t.Context(), func(tx kv.Tx) error {
		for id := range uint64(2) {
			b, err := tx.GetOne(qmdb.LeavesTable, position(id))
			if err != nil {
				return err
			}
			if len(b) != 0 {
				t.Fatal("audit wrote overlay to database")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A root-only check would miss a changed flat account. Exact value audit
	// must fail even though all QMDB commitments still reproduce.
	if err := db.Update(t.Context(), func(tx kv.RwTx) error {
		bad := a
		bad.Nonce++
		return tx.Put(modules.Account, target[:], bad.MarshalV2())
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := audit(); err == nil {
		t.Fatal("accepted mismatching PlainState")
	}
	if err := db.Update(t.Context(), func(tx kv.RwTx) error { return tx.Put(modules.Account, target[:], a.MarshalV2()) }); err != nil {
		t.Fatal(err)
	}
	if err := db.Update(t.Context(), func(tx kv.RwTx) error { return tx.Put(qmdb.LeavesTable, position(0), []byte{}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := audit(); err == nil {
		t.Fatal("treated an existing empty blob as missing")
	}
	if err := db.Update(t.Context(), func(tx kv.RwTx) error { return tx.Delete(qmdb.LeavesTable, position(0)) }); err != nil {
		t.Fatal(err)
	}
	var activeBlob []byte
	if err := db.Update(t.Context(), func(tx kv.RwTx) error {
		b, err := tx.GetOne(qmdb.LeavesTable, position(2))
		if err != nil {
			return err
		}
		activeBlob = append([]byte(nil), b...)
		return tx.Delete(qmdb.LeavesTable, position(2))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := audit(); err == nil {
		t.Fatal("overlaid a partial genesis twig")
	}
	if err := db.Update(t.Context(), func(tx kv.RwTx) error { return tx.Put(qmdb.LeavesTable, position(2), activeBlob) }); err != nil {
		t.Fatal(err)
	}
	var entryKey, entryValue []byte
	if err := db.Update(t.Context(), func(tx kv.RwTx) error {
		key := position(report.NextSlot - 1)
		b, err := tx.GetOne(qmdb.EntryTable, key)
		if err != nil {
			return err
		}
		entryKey = key
		entryValue = append([]byte(nil), b...)
		bad := append([]byte(nil), b...)
		bad[len(bad)-1] ^= 1
		return tx.Put(qmdb.EntryTable, key, bad)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := audit(); err == nil {
		t.Fatal("accepted live entry differing from committed leaf")
	}
	if err := db.Update(t.Context(), func(tx kv.RwTx) error { return tx.Put(qmdb.EntryTable, entryKey, entryValue) }); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{qmdb.TwigTable, qmdb.LeavesTable, qmdb.EntryTable} {
		if err := db.Update(t.Context(), func(tx kv.RwTx) error { return tx.Put(table, position(report.NextSlot), []byte{1}) }); err != nil {
			t.Fatal(err)
		}
		if _, err := audit(); err == nil {
			t.Fatalf("accepted out-of-range %s row", table)
		}
		if err := db.Update(t.Context(), func(tx kv.RwTx) error { return tx.Delete(table, position(report.NextSlot)) }); err != nil {
			t.Fatal(err)
		}
	}
	// Existing conflicting bytes cannot be replaced by a good genesis blob.
	if err := db.Update(t.Context(), func(tx kv.RwTx) error { return tx.Put(qmdb.LeavesTable, position(0), []byte("bad")) }); err != nil {
		t.Fatal(err)
	}
	if _, err := audit(); err == nil {
		t.Fatal("replaced an existing conflicting blob")
	}
}

func TestAuditPlainStorageExactMatching(t *testing.T) {
	db := auditTestDB(t)
	addr := types.Address{19: 7}
	slot := types.Hash{31: 9}
	key := append(append([]byte(nil), addr[:]...), slot[:]...)
	kh := qmdb.Hash(commitment.StorageKeyHash(addr, slot))
	if err := db.Update(t.Context(), func(tx kv.RwTx) error { return tx.Put(modules.Storage, key, []byte{42}) }); err != nil {
		t.Fatal(err)
	}
	check := func(expected byte) error {
		return db.View(t.Context(), func(tx kv.Tx) error {
			v := make([]byte, 32)
			v[31] = expected
			return auditPlainState(tx, map[qmdb.Hash]liveValue{kh: {value: v}})
		})
	}
	if err := check(42); err != nil {
		t.Fatal(err)
	}
	if err := check(43); err == nil {
		t.Fatal("accepted wrong storage value")
	}
	if err := db.Update(t.Context(), func(tx kv.RwTx) error { return tx.Delete(modules.Storage, key) }); err != nil {
		t.Fatal(err)
	}
	if err := check(42); err == nil {
		t.Fatal("accepted missing plain storage")
	}
	if err := db.Update(t.Context(), func(tx kv.RwTx) error { return tx.Put(modules.Storage, key[:19], []byte{42}) }); err != nil {
		t.Fatal(err)
	}
	if err := check(42); err == nil {
		t.Fatal("silently skipped unsupported storage layout")
	}
}
