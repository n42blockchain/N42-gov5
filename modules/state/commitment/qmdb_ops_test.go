// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"bytes"
	"math/rand"
	"slices"
	"sort"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/qmdb"
)

// TestSortOpsByKeyMatchesBytesCompare: the radix path and the small path
// give exactly the bytes.Compare order of the key hashes, also when many
// keys share a 16-bit prefix (the fallback bucket).
func TestSortOpsByKeyMatchesBytesCompare(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	for _, n := range []int{0, 1, 3, 500, opsRadixMin - 1, opsRadixMin, 40000} {
		for _, crowd := range []bool{false, true} {
			ops := make([]qmdb.Op, n)
			for i := range ops {
				r.Read(ops[i].KeyHash[:])
				if crowd && i%2 == 0 {
					ops[i].KeyHash[0], ops[i].KeyHash[1] = 0x12, 0x34
				}
				ops[i].Value = []byte{byte(i), byte(i >> 8), byte(i >> 16)}
			}
			want := slices.Clone(ops)
			sort.Slice(want, func(i, j int) bool { return bytes.Compare(want[i].KeyHash[:], want[j].KeyHash[:]) < 0 })
			sortOpsByKey(ops)
			for i := range ops {
				if ops[i].KeyHash != want[i].KeyHash || !bytes.Equal(ops[i].Value, want[i].Value) {
					t.Fatalf("n=%d crowd=%v: op %d differs", n, crowd, i)
				}
			}
		}
	}
}

// TestComputeRootParallelOpsMatchesSequential: a block-sized dirty set
// (above the parallel and radix thresholds, with deletes) gives the root a
// tree reaches applying the same ops built and sorted one by one.
func TestComputeRootParallelOpsMatchesSequential(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	rc := NewQMDBRootComputer()
	ref := qmdb.New()
	for blk := 0; blk < 3; blk++ {
		accounts := make(map[types.Address]*account.StateAccount, 20000)
		for i := 0; i < 20000; i++ {
			var a types.Address
			r.Read(a[:])
			if i%97 == 0 {
				accounts[a] = nil // delete
				continue
			}
			accounts[a] = &account.StateAccount{Nonce: uint64(i), Balance: *uint256.NewInt(uint64(i + 1)), Initialised: true}
		}
		var ops []qmdb.Op
		for addr, acct := range accounts {
			op := qmdb.Op{KeyHash: qmdb.Hash(AccountKeyHash(addr))}
			if acct != nil && !isAccountEmpty(acct) {
				op.Value = EncodeAccountValue(acct)
			}
			ops = append(ops, op)
		}
		sort.Slice(ops, func(i, j int) bool { return bytes.Compare(ops[i].KeyHash[:], ops[j].KeyHash[:]) < 0 })
		ref.ApplyOps(ops)
		got, err := rc.ComputeRoot(accounts, nil)
		if err != nil {
			t.Fatal(err)
		}
		if want := types.Hash(ref.Root()); got != want {
			t.Fatalf("block %d: root %x, sequential ops give %x", blk, got[:8], want[:8])
		}
	}
}
