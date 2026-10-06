package commitment

import (
	"context"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
)

// The only persisted slot is outside the fallback probes (0 and 1). Wrapping
// a reader must not turn a colliding CREATE address into an empty-storage one.
func TestQMDBReaderPreservesStorageCollision(t *testing.T) {
	for _, mode := range []QMDBReadMode{QMDBReadOff, QMDBReadVerify, QMDBReadOn} {
		t.Run(map[QMDBReadMode]string{QMDBReadOff: "off", QMDBReadVerify: "verify", QMDBReadOn: "on"}[mode], func(t *testing.T) {
			db := memdb.NewTestDB(t)
			tx, err := db.BeginRw(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			addr, slot := types.Address{19: 0xAA}, types.Hash{31: 42}
			acc := mkAccount(0, 1)
			if err := tx.Put(modules.Account, addr[:], acc.MarshalV2()); err != nil {
				t.Fatal(err)
			}
			if err := tx.Put(modules.Storage, modules.PlainGenerateCompositeStorageKey(addr[:], slot[:]), []byte{7}); err != nil {
				t.Fatal(err)
			}
			src := mapSource{qmdb.Hash(AccountKeyHash(addr)): acc.MarshalV2(), qmdb.Hash(StorageKeyHash(addr, slot)): {7}}
			r := NewQMDBStateReader(src, state.NewPlainStateReader(tx), mode)
			if !state.New(r).HasNonEmptyStorage(addr) {
				t.Fatal("QMDB wrapper hid persisted storage from CREATE collision detection")
			}
			// SELFDESTRUCT must also remove untouched slots from the commitment.
			rc := NewQMDBRootComputer()
			if _, err := rc.ComputeRoot(map[types.Address]*account.StateAccount{addr: acc},
				map[types.Address]map[types.Hash]*uint256.Int{addr: {slot: uint256.NewInt(7)}}); err != nil {
				t.Fatal(err)
			}
			ibs := state.New(r)
			ibs.SetRootComputer(rc)
			if !ibs.Selfdestruct(addr) {
				t.Fatal("selfdestruct failed")
			}
			ibs.IntermediateRoot()
			if _, live := rc.Tree().Get(qmdb.Hash(StorageKeyHash(addr, slot))); live {
				t.Fatal("selfdestruct left an untouched slot in QMDB")
			}
		})
	}
}

type enumerationFailure struct {
	fakePlain
	err error
}

func (r *enumerationFailure) ForEachStorage(types.Address, func(types.Hash, []byte) bool) error {
	return r.err
}

func TestQMDBReaderEnumerationErrors(t *testing.T) {
	errRead := errors.New("storage scan failed")
	for _, inner := range []plainReader{&fakePlain{}, &enumerationFailure{err: errRead}} {
		r := NewQMDBStateReader(nil, inner, QMDBReadOff)
		err := r.ForEachStorage(types.Address{}, func(types.Hash, []byte) bool {
			t.Fatal("unexpected storage callback")
			return false
		})
		want := state.ErrNoStorageEnumeration
		if _, ok := inner.(*enumerationFailure); ok {
			want = errRead
		}
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
	}
}
