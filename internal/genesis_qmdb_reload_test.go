package internal

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// A genesis wider than one twig must preserve frozen leaves before its first
// reload evicts sealed twig heaps. Updating an old account then reclaims its
// entry row, so that row cannot be the only source of its committed leaf hash.
func TestQMDBGenesisReloadAfterSealedAccountUpdate(t *testing.T) {
	modules.N42Init()
	dir := t.TempDir()
	open := func() kv.RwDB {
		t.Helper()
		db, err := mdbx.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).
			WithTableCfg(func(kv.TableCfg) kv.TableCfg { return modules.N42TableCfg }).
			MapSize(256 * datasize.MB).GrowthStep(4 * datasize.MB).Open(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	db := open()
	defer func() { db.Close() }()
	var target types.Address
	var targetKey qmdb.Hash
	var genesisRoot types.Hash
	if err := db.Update(t.Context(), func(tx kv.RwTx) error {
		for i := range 6000 {
			var addr types.Address
			addr[0] = 0x42
			binary.BigEndian.PutUint64(addr[12:], uint64(i))
			acct := account.NewAccount()
			acct.Balance.SetUint64(1000000)
			if err := tx.Put(modules.Account, addr[:], acct.MarshalV2()); err != nil {
				return err
			}
			key := qmdb.Hash(commitment.AccountKeyHash(addr))
			if i == 0 || bytes.Compare(key[:], targetKey[:]) < 0 {
				target, targetKey = addr, key
			}
		}
		want, err := QMDBGenesisRoot(tx)
		if err != nil {
			return err
		}
		genesisRoot, err = SeedQMDBGenesis(tx)
		if err == nil && genesisRoot != want {
			t.Fatal("persisting genesis changed its header root")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db = open()
	load := func() *commitment.QMDBRootComputer {
		t.Helper()
		rc := commitment.NewQMDBRootComputer()
		if err := db.View(t.Context(), func(tx kv.Tx) error {
			rc.SetCold(tx)
			defer rc.SetCold(nil)
			return rc.LoadFrom(tx)
		}); err != nil {
			t.Fatalf("reload persisted forest: %v", err)
		}
		return rc
	}
	rc := load()
	if rc.Root() != genesisRoot {
		t.Fatal("first restart changed the genesis root")
	}
	slot, ok := rc.Tree().IndexLookup(targetKey)
	if !ok || slot >= qmdb.TwigSize {
		t.Fatalf("target must occupy the first sealed twig, got %d/%v", slot, ok)
	}
	updated := account.NewAccount()
	updated.Nonce = 1
	updated.Balance.SetUint64(999999)
	var updatedRoot types.Hash
	if err := db.Update(t.Context(), func(tx kv.RwTx) error {
		rc.SetCold(tx)
		var err error
		updatedRoot, err = rc.ComputeRoot(map[types.Address]*account.StateAccount{target: &updated}, nil)
		if err != nil {
			return err
		}
		if err := tx.Put(modules.Account, target[:], updated.MarshalV2()); err != nil {
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
	db.Close()
	db = open()
	reloaded := load()
	if reloaded.Root() != updatedRoot || reloaded.Tree().LiveCount() != 6000 {
		t.Fatal("second restart changed the updated root or live account count")
	}
	if err := db.View(t.Context(), func(tx kv.Tx) error {
		var oldKey [8]byte
		binary.BigEndian.PutUint64(oldKey[:], slot)
		old, err := tx.GetOne(qmdb.EntryTable, oldKey[:])
		if err != nil {
			return err
		}
		if len(old) != 0 {
			t.Fatal("test did not exercise reclamation of the old genesis entry")
		}
		// Both fully sealed genesis twigs need independent frozen leaf blobs.
		for id := range uint64(2) {
			var key [8]byte
			binary.BigEndian.PutUint64(key[:], id)
			blob, err := tx.GetOne(qmdb.LeavesTable, key[:])
			if err != nil {
				return err
			}
			if len(blob) == 0 {
				t.Fatalf("genesis twig %d has no persisted leaf blob", id)
			}
		}
		enc, found, evicted := reloaded.Lookup(targetKey, tx)
		var got account.StateAccount
		if !found || evicted {
			t.Fatalf("updated account missing after restart: found=%v evicted=%v", found, evicted)
		}
		if err := got.DecodeForStorage(enc); err != nil {
			return err
		}
		if got.Nonce != updated.Nonce || got.Balance.Cmp(&updated.Balance) != 0 {
			t.Fatal("updated nonce/balance changed after second restart")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
