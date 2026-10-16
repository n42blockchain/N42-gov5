// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/parallel"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

type bulkMapReader map[types.Address]*account.StateAccount

func (r bulkMapReader) ReadAccountData(a types.Address) (*account.StateAccount, error) {
	acc, ok := r[a]
	if !ok {
		return nil, nil
	}
	c := new(account.StateAccount)
	c.Copy(acc)
	return c, nil
}
func (bulkMapReader) ReadAccountStorage(types.Address, *types.Hash) ([]byte, error) {
	return nil, nil
}
func (bulkMapReader) ReadAccountCode(types.Address, types.Hash) ([]byte, error) { return nil, nil }
func (bulkMapReader) ReadAccountCodeSize(types.Address, types.Hash) (int, error) {
	return 0, nil
}

// bulkKnownCache is a state.KnownAccounts over a map (nil: absent).
type bulkKnownCache map[types.Address]*account.StateAccount

func (k bulkKnownCache) Peek(a types.Address) (*account.StateAccount, bool) {
	acc, ok := k[a]
	return acc, ok
}
func (k bulkKnownCache) Get(a types.Address) (*account.StateAccount, bool) {
	acc, ok := k[a]
	if !ok || acc == nil {
		return nil, ok
	}
	c := *acc
	return &c, true
}

// recWriter records CommitBlock's writes.
type recWriter struct{ log []string }

func (w *recWriter) UpdateAccountData(a types.Address, o, n *account.StateAccount) error {
	w.log = append(w.log, fmt.Sprintf("upd %x %d/%s -> %d/%s %x", a, o.Nonce, o.Balance.String(), n.Nonce, n.Balance.String(), n.CodeHash))
	return nil
}
func (w *recWriter) UpdateAccountCode(a types.Address, h types.Hash, _ []byte) error {
	w.log = append(w.log, fmt.Sprintf("code %x %x", a, h))
	return nil
}
func (w *recWriter) DeleteAccount(a types.Address, _ *account.StateAccount) error {
	w.log = append(w.log, fmt.Sprintf("del %x", a))
	return nil
}
func (w *recWriter) WriteAccountStorage(a types.Address, k types.Hash, o, v uint256.Int) error {
	w.log = append(w.log, fmt.Sprintf("sto %x %x %s %s", a, k, o.String(), v.String()))
	return nil
}
func (w *recWriter) CreateContract(a types.Address) error {
	w.log = append(w.log, fmt.Sprintf("create %x", a))
	return nil
}

func bulkTestAddr(i int) types.Address {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(i))
	h := sha256.Sum256(append([]byte("apply-bulk"), b[:]...))
	return types.BytesToAddress(h[:20])
}

// The same MVS through applyMVSToIBS's per-entry Pass 1 and its bulk pass
// (P13c) must leave the same dirty set and commit the same rows: written,
// credited (zero credits included), deleted, wiped-and-recreated accounts
// with storage and code, accounts absent from the base state, accounts the
// known cache does not hold, and an account already live before the fold.
func TestApplyMVSBulkMatchesPerEntry(t *testing.T) {
	const n = 9000
	const numTxs = 40_000 // enough for several collect workers
	store := bulkMapReader{}
	known := bulkKnownCache{}
	mvs := parallel.NewMVS()
	var live types.Address
	for i := 0; i < n; i++ {
		a := bulkTestAddr(i)
		if i%9 != 0 {
			acc := account.NewAccount()
			acc.Balance.SetUint64(uint64(10_000 + i))
			acc.Nonce = uint64(i % 4)
			store[a] = &acc
			if i%29 != 0 {
				known[a] = &acc
			}
		} else if i%29 != 0 {
			known[a] = nil
		}
		tx := i % numTxs
		bal := parallel.LocationKey{Address: a, Field: parallel.FieldBalance}
		switch i % 7 {
		case 0, 1, 2, 3:
			acc := account.NewAccount()
			acc.Balance.SetUint64(uint64(i))
			acc.Nonce = uint64(i%4 + 1)
			mvs.Write(bal, tx, 0, acc.MarshalV2())
			if i%13 == 0 {
				var slot types.Hash
				slot[31] = byte(i)
				mvs.Write(parallel.LocationKey{Address: a, Field: parallel.FieldStorage, Slot: slot}, tx, 0, []byte{byte(i), 1})
			}
			if i%17 == 0 {
				mvs.Write(parallel.LocationKey{Address: a, Field: parallel.FieldCode}, tx, 0, []byte{0x60, byte(i)})
			}
			if i%41 == 0 {
				mvs.Write(parallel.LocationKey{Address: a, Field: parallel.FieldStorageWipe}, tx, 0, []byte{1})
			}
			if live == (types.Address{}) && i > 100 && store[a] != nil {
				live = a
			}
		case 4, 5:
			mvs.WriteDelta(bal, tx, 0, uint256.NewInt(uint64(i%3)))
		case 6:
			mvs.Write(bal, tx, 0, nil)
		}
	}

	run := func(bulk bool) ([]string, []string) {
		prev := applyBulkEnabled
		applyBulkEnabled = bulk
		defer func() { applyBulkEnabled = prev }()
		pf := state.NewAccountPrefetch(store)
		pf.SetKnown(known)
		ibs := state.New(pf)
		ibs.SetAccountPrefetch(pf)
		ibs.GetBalance(live) // touched before the fold (a system call)
		if err := applyMVSToIBS(mvs, numTxs, ibs); err != nil {
			t.Fatal(err)
		}
		dirty := ibs.DirtyAddresses()
		ds := make([]string, len(dirty))
		for i, a := range dirty {
			ds[i] = a.Hex()
		}
		sort.Strings(ds)
		rules := &params.Rules{IsSpuriousDragon: true}
		if err := ibs.FinalizeTx(rules, state.NewNoopWriter()); err != nil {
			t.Fatal(err)
		}
		w := &recWriter{}
		if err := ibs.CommitBlock(rules, w); err != nil {
			t.Fatal(err)
		}
		sort.Strings(w.log)
		return ds, w.log
	}
	wantDirty, wantRows := run(false)
	gotDirty, gotRows := run(true)
	if len(wantRows) < n/2 {
		t.Fatalf("only %d rows committed", len(wantRows))
	}
	if !reflect.DeepEqual(wantDirty, gotDirty) {
		t.Fatalf("dirty sets differ: %d vs %d", len(wantDirty), len(gotDirty))
	}
	if !reflect.DeepEqual(wantRows, gotRows) {
		for i := range wantRows {
			if i >= len(gotRows) || wantRows[i] != gotRows[i] {
				t.Fatalf("committed rows differ at %d: %q", i, wantRows[i])
			}
		}
		t.Fatalf("committed rows differ: %d vs %d", len(wantRows), len(gotRows))
	}
}
