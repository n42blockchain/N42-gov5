// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"math/rand"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// blockShapedDirtySet builds a synthetic dirty set the size of one 163k-
// transfer block's own mutation set: ~31k touched accounts (6cw/6dx: a
// 163,000-transaction block against a ~22,857-address hot recipient set
// produces ~31k distinct dirty accounts, not 163k*2 -- most transactions
// re-touch the same small recipient set).
func blockShapedDirtySet() map[types.Address]*account.StateAccount {
	const dirtyAccounts = 31000
	rnd := rand.New(rand.NewSource(1))
	accts := make(map[types.Address]*account.StateAccount, dirtyAccounts)
	for i := 0; i < dirtyAccounts; i++ {
		var a types.Address
		rnd.Read(a[:])
		acc := &account.StateAccount{Initialised: true, Nonce: uint64(i)}
		acc.Balance.SetUint64(uint64(rnd.Int63()))
		accts[a] = acc
	}
	return accts
}

// BenchmarkQMDBRootComputeTwice is "before": the leader's own shape today --
// the SAME dirty set applied and folded on two SEPARATE tree instances (the
// isolated speculative tree during the build, the live tree during the
// write), exactly what docs/QS_BLOCK_TIME_BUDGET.md 6f8/6f9 measured at
// ~58-59ms each on the real 163k-transfer fleet block (~117ms total).
// Pinned: taskset -c 200-207 nice -n 19 go test -tags nosqlite,noboltdb
// -run NONE -bench BenchmarkQMDBRootCompute -benchtime=3x -benchmem
// ./modules/state/commitment/
func BenchmarkQMDBRootComputeTwice(b *testing.B) {
	accts := blockShapedDirtySet()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		isolated := NewQMDBRootComputer()
		if _, err := isolated.ComputeRoot(accts, nil); err != nil {
			b.Fatal(err)
		}
		live := NewQMDBRootComputer()
		if _, err := live.ComputeRoot(accts, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkQMDBRootComputeOnce is "after": the same dirty set computed on
// ONE tree instance only -- the achievable shape once a later round trusts
// the isolated build's own root and skips the live tree's independent fold
// (N42_QMDB_SINGLE_FOLD's own v1 does not yet do this in production -- see
// ComputeRootShared's own doc comment -- this benchmark measures the
// opportunity the switch is a safety-proving step toward).
func BenchmarkQMDBRootComputeOnce(b *testing.B) {
	accts := blockShapedDirtySet()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rc := NewQMDBRootComputer()
		if _, err := rc.ComputeRoot(accts, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkQMDBRootComputeShared is v1's OWN actual production shape:
// ComputeRootShared still applies and folds (matching Twice's own cost) but
// through the explicit guard/counter path -- confirms the switch adds no
// measurable overhead of its own beyond the (unavoidable, this round)
// second computation.
func BenchmarkQMDBRootComputeShared(b *testing.B) {
	accts := blockShapedDirtySet()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		isolated := NewQMDBRootComputer()
		sealedRoot, err := isolated.ComputeRoot(accts, nil)
		if err != nil {
			b.Fatal(err)
		}
		live := NewQMDBRootComputer()
		if _, _, err := live.ComputeRootShared(accts, nil, sealedRoot); err != nil {
			b.Fatal(err)
		}
	}
}
