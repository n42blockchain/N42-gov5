// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// TestG52WalkAsOfAccounts walks the account changeset/history over two
// accounts: one changed at block 5 (old balance 10 -> new balance 20) and
// one untouched since genesis. As of block 5 (query timestamp 5) the
// changed account must read its OLD (pre-change) value via the history
// path; as of block 6 it must read the tip value via the plain-cursor fast
// path. Neither branch is reached by any other test in the package.
func TestG52WalkAsOfAccounts(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevTables })
	modules.SetPlainAccountWriteSkipped(false)
	t.Cleanup(func() { modules.SetPlainAccountWriteSkipped(false) })

	changed := types.HexToAddress("0x1000000000000000000000000000000000000041")
	untouched := types.HexToAddress("0x1000000000000000000000000000000000000042")

	db := memdb.NewTestDB(t)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		w := NewPlainStateWriter(tx, tx, 5)
		old := account.NewAccount()
		old.Initialised = true
		old.Nonce = 1
		old.Balance = *uint256.NewInt(10)
		cur := account.NewAccount()
		cur.Initialised = true
		cur.Nonce = 2
		cur.Balance = *uint256.NewInt(20)
		if err := w.UpdateAccountData(changed, &old, &cur); err != nil {
			return err
		}
		if err := w.csw.WriteChangeSets(); err != nil {
			return err
		}
		if err := w.csw.WriteHistory(); err != nil {
			return err
		}

		// Untouched account: written directly to current state, no history.
		u := account.NewAccount()
		u.Initialised = true
		u.Nonce = 9
		u.Balance = *uint256.NewInt(999)
		return tx.Put(modules.Account, untouched[:], u.MarshalV2())
	}); err != nil {
		t.Fatal(err)
	}

	type hit struct {
		addr    types.Address
		balance uint64
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var got []hit
		err := WalkAsOfAccounts(tx, types.Address{}, 5, func(k, v []byte) (bool, error) {
			var a account.StateAccount
			if err := a.DecodeForStorage(v); err != nil {
				return false, err
			}
			var addr types.Address
			copy(addr[:], k)
			got = append(got, hit{addr: addr, balance: a.Balance.Uint64()})
			return true, nil
		})
		if err != nil {
			t.Fatalf("WalkAsOfAccounts(@5): %v", err)
		}
		want := map[types.Address]uint64{changed: 10, untouched: 999}
		if len(got) != len(want) {
			t.Fatalf("WalkAsOfAccounts(@5) returned %d entries, want %d: %+v", len(got), len(want), got)
		}
		for _, h := range got {
			if want[h.addr] != h.balance {
				t.Fatalf("addr %v balance=%d, want %d", h.addr, h.balance, want[h.addr])
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var got []hit
		err := WalkAsOfAccounts(tx, types.Address{}, 6, func(k, v []byte) (bool, error) {
			var a account.StateAccount
			if err := a.DecodeForStorage(v); err != nil {
				return false, err
			}
			var addr types.Address
			copy(addr[:], k)
			got = append(got, hit{addr: addr, balance: a.Balance.Uint64()})
			return true, nil
		})
		if err != nil {
			t.Fatalf("WalkAsOfAccounts(@6): %v", err)
		}
		want := map[types.Address]uint64{changed: 20, untouched: 999}
		for _, h := range got {
			if want[h.addr] != h.balance {
				t.Fatalf("addr %v balance=%d, want %d (tip)", h.addr, h.balance, want[h.addr])
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestG52WalkAsOfStorage mirrors TestG52WalkAsOfAccounts for a single
// account's storage: one slot changed at block 5 (old=0x07 -> new=0x0e) and
// one slot untouched since genesis. As-of block 5 the changed slot must
// read the old value from the history/changeset path; as-of block 6 it
// reads the tip value from the plain cursor.
func TestG52WalkAsOfStorage(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevTables })

	addr := types.HexToAddress("0x1000000000000000000000000000000000000043")
	changedSlot := types.HexToHash("0x01")
	untouchedSlot := types.HexToHash("0x02")

	db := memdb.NewTestDB(t)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		w := NewPlainStateWriter(tx, tx, 5)
		if err := w.WriteAccountStorage(addr, changedSlot, *uint256.NewInt(7), *uint256.NewInt(14)); err != nil {
			return err
		}
		if err := w.csw.WriteChangeSets(); err != nil {
			return err
		}
		if err := w.csw.WriteHistory(); err != nil {
			return err
		}

		// Untouched slot: written directly, no history entry.
		compositeKey := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), untouchedSlot.Bytes())
		return tx.Put(modules.Storage, compositeKey, []byte{0x2a})
	}); err != nil {
		t.Fatal(err)
	}

	type hit struct {
		slot  types.Hash
		value []byte
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var got []hit
		err := WalkAsOfStorage(tx, addr, types.Hash{}, 5, func(k1, k2, v []byte) (bool, error) {
			var slot types.Hash
			copy(slot[:], k2)
			cp := append([]byte(nil), v...)
			got = append(got, hit{slot: slot, value: cp})
			return true, nil
		})
		if err != nil {
			t.Fatalf("WalkAsOfStorage(@5): %v", err)
		}
		found := map[types.Hash][]byte{}
		for _, h := range got {
			found[h.slot] = h.value
		}
		if v, ok := found[changedSlot]; !ok || uint256.NewInt(0).SetBytes(v).Uint64() != 7 {
			t.Fatalf("changed slot @5 = %x, want old value 7", v)
		}
		if v, ok := found[untouchedSlot]; !ok || v[0] != 0x2a {
			t.Fatalf("untouched slot @5 = %x, want 2a", v)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var got []hit
		err := WalkAsOfStorage(tx, addr, types.Hash{}, 6, func(k1, k2, v []byte) (bool, error) {
			var slot types.Hash
			copy(slot[:], k2)
			cp := append([]byte(nil), v...)
			got = append(got, hit{slot: slot, value: cp})
			return true, nil
		})
		if err != nil {
			t.Fatalf("WalkAsOfStorage(@6): %v", err)
		}
		found := map[types.Hash][]byte{}
		for _, h := range got {
			found[h.slot] = h.value
		}
		if v, ok := found[changedSlot]; !ok || uint256.NewInt(0).SetBytes(v).Uint64() != 14 {
			t.Fatalf("changed slot @6 = %x, want tip value 14", v)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
