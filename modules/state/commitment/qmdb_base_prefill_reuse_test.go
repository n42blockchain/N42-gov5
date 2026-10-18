// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"bytes"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
)

// TestBasePrefillReuseMatchesFresh: fills whose maps and value arenas come
// from released prefills answer exactly what fresh fills answer -- value,
// found and held for every key, absent ones included -- for key sets of
// other sizes and contents, and a released prefill answers nothing (its
// source reads the tree).
func TestBasePrefillReuseMatchesFresh(t *testing.T) {
	prevCfg := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevCfg })
	db := memdb.NewTestDB(t)
	rc := NewQMDBRootComputer()
	accts := map[types.Address]*account.StateAccount{}
	stor := map[types.Address]map[types.Hash]*uint256.Int{}
	addr := func(i int) types.Address { return types.Address{0xb0, byte(i), byte(i >> 8)} }
	for i := 0; i < 600; i++ {
		accts[addr(i)] = mkAccount(uint64(i), uint64(1000+i))
		if i%7 == 0 {
			stor[addr(i)] = map[types.Hash]*uint256.Int{{byte(i)}: uint256.NewInt(uint64(i + 1))}
		}
	}
	if _, err := rc.ComputeRoot(accts, stor); err != nil {
		t.Fatal(err)
	}
	keysFor := func(round int) [][]qmdb.Hash {
		var a, b []qmdb.Hash
		for i := round * 37 % 200; i < 600+round*11; i += 1 + round%3 {
			// Present accounts, absent ones past 600, slots, duplicates.
			a = append(a, qmdb.Hash(AccountKeyHash(addr(i))))
			if i%7 == 0 {
				b = append(b, qmdb.Hash(StorageKeyHash(addr(i), types.Hash{byte(i)})), qmdb.Hash(AccountKeyHash(addr(i))))
			}
		}
		return [][]qmdb.Hash{a, b}
	}
	type answer struct {
		v         []byte
		found, ok bool
	}
	answers := func(pf *BasePrefill, keys [][]qmdb.Hash) map[qmdb.Hash]answer {
		out := map[qmdb.Hash]answer{}
		for _, ks := range keys {
			for _, k := range ks {
				v, found, ok := pf.get(k)
				out[k] = answer{append([]byte(nil), v...), found, ok}
			}
		}
		return out
	}
	waitKept := func() {
		for i := 0; i < 200; i++ {
			prefillFree.mu.Lock()
			n := len(prefillFree.list)
			prefillFree.mu.Unlock()
			if n > 0 {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("a released prefill was not kept")
	}
	for round := 0; round < 6; round++ {
		keys := keysFor(round)
		restore := SetPrefillScratchPoolForTest(false)
		fresh, err := rc.FillBasePrefill(db, keys, 3, nil)
		restore()
		if err != nil {
			t.Fatal(err)
		}
		want := answers(fresh, keys)

		restore = SetPrefillScratchPoolForTest(true)
		pf, err := rc.FillBasePrefill(db, keys, 3, nil)
		if err != nil {
			t.Fatal(err)
		}
		got := answers(pf, keys)
		if pf.Len() != fresh.Len() || len(got) != len(want) {
			t.Fatalf("round %d: %d keys reused, %d fresh", round, pf.Len(), fresh.Len())
		}
		for k, w := range want {
			if g := got[k]; g.found != w.found || g.ok != w.ok || !bytes.Equal(g.v, w.v) {
				t.Fatalf("round %d: key %x: reused %+v, fresh %+v", round, k[:4], g, w)
			}
		}
		pf.Release()
		pf.Release() // idempotent
		if _, _, ok := pf.get(keys[0][0]); ok {
			t.Fatalf("round %d: a released prefill answered", round)
		}
		waitKept()
		restore()
	}
}
