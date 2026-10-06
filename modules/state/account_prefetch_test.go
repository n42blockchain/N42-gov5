// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// countingReader serves a fixed account set and counts store reads.
type countingReader struct {
	accts map[types.Address]*account.StateAccount
	reads int
}

func (r *countingReader) ReadAccountData(a types.Address) (*account.StateAccount, error) {
	r.reads++
	if acc, ok := r.accts[a]; ok {
		c := new(account.StateAccount)
		c.Copy(acc)
		return c, nil
	}
	return nil, nil
}
func (r *countingReader) ReadAccountStorage(types.Address, *types.Hash) ([]byte, error) {
	return nil, nil
}
func (r *countingReader) ReadAccountCode(types.Address, types.Hash) ([]byte, error) { return nil, nil }
func (r *countingReader) ReadAccountCodeSize(types.Address, types.Hash) (int, error) {
	return 0, nil
}

// A delta-credited account (AddBalance without a prior read) is folded at
// the block end through the reader. With the prefetch layer seeded, the
// fold's read is a hit, the balance is old + delta, an account seeded as
// absent is created with just the delta, and the store sees no read.
func TestAccountPrefetchServesTheFold(t *testing.T) {
	var known, absent types.Address
	known[19], absent[19] = 1, 2
	store := &countingReader{accts: map[types.Address]*account.StateAccount{
		known: {Initialised: true, Nonce: 7, Balance: *uint256.NewInt(1000)},
	}}
	pf := NewAccountPrefetch(store)
	ibs := New(pf)
	ibs.SetAccountPrefetch(pf)

	ibs.AddBalance(known, uint256.NewInt(5))
	ibs.AddBalance(absent, uint256.NewInt(9))
	pending := ibs.PendingBalanceIncreases()
	if len(pending) != 2 {
		t.Fatalf("pending increases: %d, want 2", len(pending))
	}

	// What the parallel prefetch would produce: the store's view of both.
	seed := map[types.Address]*account.StateAccount{}
	for _, a := range pending {
		acc, _ := store.ReadAccountData(a)
		seed[a] = acc
	}
	store.reads = 0
	pf.Seed(seed)

	if got := ibs.GetBalance(known); got.Uint64() != 1005 {
		t.Fatalf("known balance %s, want 1005", got)
	}
	if got := ibs.GetNonce(known); got != 7 {
		t.Fatalf("known nonce %d, want 7", got)
	}
	if got := ibs.GetBalance(absent); got.Uint64() != 9 {
		t.Fatalf("absent balance %s, want 9", got)
	}
	if store.reads != 0 {
		t.Fatalf("store reads during the fold: %d, want 0", store.reads)
	}
	if pf.Hits() != 2 {
		t.Fatalf("prefetch hits %d, want 2", pf.Hits())
	}
	if len(ibs.PendingBalanceIncreases()) != 0 {
		t.Fatal("increases still pending after the fold")
	}
	// A seeded value cannot be poisoned by a caller mutating what it got.
	seed[known].Balance.SetUint64(1)
	acc, _ := pf.ReadAccountData(known)
	if acc.Balance.Uint64() != 1 {
		t.Fatalf("seed mutation not observed (copy semantics on Seed?): %s", acc.Balance.String())
	}
	// An unseeded address still reads through.
	var other types.Address
	other[19] = 3
	if _, err := pf.ReadAccountData(other); err != nil || store.reads != 1 {
		t.Fatalf("unseeded read did not fall through: reads=%d err=%v", store.reads, err)
	}
}

// The layer walk that recovers post-state layers sees through the prefetch
// layer.
func TestPostStateLayersSeeThroughPrefetch(t *testing.T) {
	store := &countingReader{}
	post := &PostState{}
	var r StateReader = NewPostStateReader(post, store)
	r = NewAccountPrefetch(r)
	layers, base := PostStateLayers(r)
	if len(layers) != 1 || layers[0] != post || base != StateReader(store) {
		t.Fatalf("walk through the prefetch layer: %d layers, base %T", len(layers), base)
	}
}
