// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Regression test for the concurrent-vs-sequential MPT root divergence:
// on an incremental (second/third-call) concurrent run where a root row-0
// cell carries an extension (all keys under one top nibble share the second
// nibble), foldNibble used to unconditionally trim the mount nibble off a
// grid[0][nib] slot cell that already excluded it, underflowing hashedExtLen
// and producing a silently wrong stateRoot. See the fromRoot guard in
// hex_concurrent_patricia_hashed.go / foldMounted in hex_patricia_hashed.go.
package commitment

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// findReproAddrs brute-forces addresses:
//   - ax[0], ax[1]: hashed key nibble0 == 7 and equal nibble1 (subtree branch at depth 3
//     => root row-0 cell at nibble 7 gets a 1-nibble extension)
//   - cz[0], cz[1]: hashed key nibble0 == 0 with different nibble1 (creates a branch at
//     prefix [0] so CanDoConcurrentNext returns true and the second call stays parallel)
func findReproAddrs(t *testing.T) (ax [2]types.Address, cz [2]types.Address) {
	t.Helper()
	hasher := makeSharedHasher()
	found7 := make(map[byte][]types.Address)
	var found0 []types.Address
	seen0 := make(map[byte]bool)
	var have7 bool
	for i := 0; i < 1<<22 && !(have7 && len(found0) >= 2); i++ {
		var a types.Address
		binary.BigEndian.PutUint32(a[:4], uint32(i))
		a[19] = 1
		nib := hasher(a.Bytes())
		if nib[0] == 7 && !have7 {
			found7[nib[1]] = append(found7[nib[1]], a)
			if len(found7[nib[1]]) == 2 {
				copy(ax[:], found7[nib[1]][:2])
				have7 = true
			}
		}
		if nib[0] == 0 && !seen0[nib[1]] && len(found0) < 2 {
			seen0[nib[1]] = true
			found0 = append(found0, a)
		}
	}
	if !have7 || len(found0) < 2 {
		t.Fatalf("could not find repro addresses")
	}
	copy(cz[:], found0[:2])
	return ax, cz
}

func TestConcurrentMPTIncrementalExtension(t *testing.T) {
	ax, cz := findReproAddrs(t)

	mkAcct := func(nonce uint64, bal uint64) *account.StateAccount {
		a := &account.StateAccount{Initialised: true, Nonce: nonce}
		a.Balance.SetUint64(bal)
		return a
	}

	block1 := map[types.Address]*account.StateAccount{
		ax[0]: mkAcct(1, 100),
		ax[1]: mkAcct(2, 200),
		cz[0]: mkAcct(3, 300),
		cz[1]: mkAcct(4, 400),
	}
	// Block 2: bump ax[0]; Block 3: bump again (block 2 may run the sequential
	// fallback because CanDoConcurrentNext was evaluated before collector merge;
	// block 3 then runs truly concurrent with the row-0 extension cell present).
	block2Acct := mkAcct(99, 9999)
	block3Acct := mkAcct(100, 12345)

	ctx := context.Background()

	// Sequential
	seqDB := memdb.NewTestDB(t)
	seedPlainState(t, seqDB, block1, nil)
	seqTx1, err := seqDB.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seqM := NewMPTRootComputer()
	seqM.SetStateReader(NewPlainStateMPTReader(seqTx1))
	seqR1, err := seqM.ComputeRoot(block1, nil)
	if err != nil {
		t.Fatalf("seq block1: %v", err)
	}
	seqTx1.Rollback()
	if err := seqDB.Update(ctx, func(tx kv.RwTx) error {
		return tx.Put(modules.Account, ax[0].Bytes(), block2Acct.MarshalV2())
	}); err != nil {
		t.Fatal(err)
	}
	seqTx2, err := seqDB.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer seqTx2.Rollback()
	seqM.SetStateReader(NewPlainStateMPTReader(seqTx2))
	seqR2, err := seqM.ComputeRoot(map[types.Address]*account.StateAccount{ax[0]: block2Acct}, nil)
	if err != nil {
		t.Fatalf("seq block2: %v", err)
	}
	seqTx2.Rollback()
	if err := seqDB.Update(ctx, func(tx kv.RwTx) error {
		return tx.Put(modules.Account, ax[0].Bytes(), block3Acct.MarshalV2())
	}); err != nil {
		t.Fatal(err)
	}
	seqTx3, err := seqDB.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer seqTx3.Rollback()
	seqM.SetStateReader(NewPlainStateMPTReader(seqTx3))
	seqR3, err := seqM.ComputeRoot(map[types.Address]*account.StateAccount{ax[0]: block3Acct}, nil)
	if err != nil {
		t.Fatalf("seq block3: %v", err)
	}

	// Concurrent
	conDB := memdb.NewTestDB(t)
	seedPlainState(t, conDB, block1, nil)
	conTx1, err := conDB.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conM := NewConcurrentMPTRootComputer(conDB, t.TempDir(), log2.New())
	conM.SetStateReader(NewPlainStateMPTReader(conTx1))
	conR1, err := conM.ComputeRoot(block1, nil)
	if err != nil {
		t.Fatalf("conc block1: %v", err)
	}
	t.Logf("pending deferred root branch updates after concurrent block1: %v",
		conM.ctrie.RootTrie().HasPendingDeferredUpdates())
	conTx1.Rollback()
	if err := conDB.Update(ctx, func(tx kv.RwTx) error {
		return tx.Put(modules.Account, ax[0].Bytes(), block2Acct.MarshalV2())
	}); err != nil {
		t.Fatal(err)
	}
	conTx2, err := conDB.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conTx2.Rollback()
	conM.SetStateReader(NewPlainStateMPTReader(conTx2))
	if !conM.updates.IsConcurrentCommitment() {
		t.Logf("NOTE: second call would run SEQUENTIAL fallback (CanDoConcurrentNext=false); repro not exercised")
	}
	conR2, err := conM.ComputeRoot(map[types.Address]*account.StateAccount{ax[0]: block2Acct}, nil)
	if err != nil {
		t.Fatalf("conc block2: %v", err)
	}
	conTx2.Rollback()
	if err := conDB.Update(ctx, func(tx kv.RwTx) error {
		return tx.Put(modules.Account, ax[0].Bytes(), block3Acct.MarshalV2())
	}); err != nil {
		t.Fatal(err)
	}
	conTx3, err := conDB.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conTx3.Rollback()
	conM.SetStateReader(NewPlainStateMPTReader(conTx3))
	t.Logf("block3 will run concurrent=%v", conM.updates.IsConcurrentCommitment())
	conR3, err := conM.ComputeRoot(map[types.Address]*account.StateAccount{ax[0]: block3Acct}, nil)
	if err != nil {
		t.Fatalf("conc block3: %v", err)
	}

	if seqR1 != conR1 {
		t.Errorf("block1 mismatch: seq=%x conc=%x", seqR1[:], conR1[:])
	}
	if seqR2 != conR2 {
		t.Errorf("block2 mismatch: seq=%x conc=%x", seqR2[:], conR2[:])
	}
	if seqR3 != conR3 {
		t.Errorf("block3 mismatch (root row-0 extension case): seq=%x conc=%x", seqR3[:], conR3[:])
	} else {
		t.Logf("block3 match: %x", seqR3[:])
	}
}
