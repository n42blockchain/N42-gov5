// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"sync/atomic"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// raceEarlySource is an early snapshot of block N whose transactions only
// credited addr by delta; full is N's complete post-state once published.
type raceEarlySource struct {
	addr  types.Address
	delta uint256.Int
	full  atomic.Pointer[PostState]
}

func (s *raceEarlySource) EarlyAccount(a types.Address) (*account.StateAccount, *uint256.Int, bool, bool) {
	if a != s.addr {
		return nil, nil, false, false
	}
	d := s.delta
	return nil, &d, true, true
}
func (s *raceEarlySource) EarlyStorage(types.Address, *types.Hash) ([]byte, bool) { return nil, false }
func (s *raceEarlySource) EarlyCode(types.Address, types.Hash) ([]byte, bool)     { return nil, false }
func (s *raceEarlySource) Hold(types.Address) bool                                { return false }
func (s *raceEarlySource) Full() *PostState                                       { return s.full.Load() }
func (s *raceEarlySource) WaitFull() (*PostState, error)                          { return s.full.Load(), nil }

// advancingBase is the live store under the snapshot's layers: its first
// read lands just after N's root hook published the complete post-state
// and N's root computation applied N to the store (a reader that checked
// Full before the hook, then waited on the tree's lock behind the apply).
type advancingBase struct {
	src     *raceEarlySource
	full    *PostState
	pre     uint64 // addr's balance before N
	applied atomic.Bool
}

func (b *advancingBase) ReadAccountData(a types.Address) (*account.StateAccount, error) {
	if b.applied.CompareAndSwap(false, true) {
		b.src.full.Store(b.full) // the hook, then the apply
	}
	acc := &account.StateAccount{Initialised: true}
	acc.Balance.SetUint64(b.pre)
	if b.applied.Load() {
		acc.Balance.Add(&acc.Balance, &b.src.delta) // the store holds N
	}
	return acc, nil
}
func (b *advancingBase) ReadAccountStorage(types.Address, *types.Hash) ([]byte, error) {
	return nil, nil
}
func (b *advancingBase) ReadAccountCode(types.Address, types.Hash) ([]byte, error) { return nil, nil }
func (b *advancingBase) ReadAccountCodeSize(types.Address, types.Hash) (int, error) {
	return 0, nil
}

// TestEarlySnapshotCreditNotDoubledAcrossHook is round S141's divergence:
// a child run read a recipient its parent N only credited through N's
// early snapshot, found no complete post-state, and composed the store's
// balance with N's credit -- but the store read landed after N's hook and
// root apply, so it already held the credit, and the child's base came out
// with N's credit twice (node6, block 14137466, account 3ae6a138: +4 wei).
func TestEarlySnapshotCreditNotDoubledAcrossHook(t *testing.T) {
	addr := types.HexToAddress("0x3ae6a13809d7b89df2f6f240cb554a791365fdf2")
	src := &raceEarlySource{addr: addr}
	src.delta.SetUint64(4)
	post := &account.StateAccount{Initialised: true}
	post.Balance.SetUint64(0x2af0 + 4)
	full := &PostState{accounts: map[types.Address]*account.StateAccount{addr: post}}
	base := &advancingBase{src: src, full: full, pre: 0x2af0}
	r := NewPostStateReader(NewEarlyPostState(src), base)
	acc, err := r.ReadAccountData(addr)
	if err != nil {
		t.Fatal(err)
	}
	if acc == nil || acc.Balance.Uint64() != 0x2af0+4 {
		got := uint64(0)
		if acc != nil {
			got = acc.Balance.Uint64()
		}
		t.Fatalf("balance %#x after N, want %#x (N's credit applied once)", got, 0x2af0+4)
	}
}
