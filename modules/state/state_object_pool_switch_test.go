// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"reflect"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// withPooling runs fn under an explicit pooling mode and restores the previous
// one. The flag is process-wide, so nothing here may run in parallel.
func withPooling(t *testing.T, on bool, fn func()) {
	t.Helper()
	prev := stateObjectPooling
	stateObjectPooling = on
	defer func() { stateObjectPooling = prev }()
	fn()
}

// The pooling switch is a diagnostic: it is only useful if turning it off
// changes allocation and nothing else. If the two modes could disagree on
// state, a failure that vanished with pooling off would prove nothing, because
// the switch itself would be a candidate explanation.
func TestStateObjectPoolingModesAgree(t *testing.T) {
	addrs := []types.Address{
		types.HexToAddress("0x00000000000000000000000000000000000000a1"),
		types.HexToAddress("0x00000000000000000000000000000000000000a2"),
		types.HexToAddress("0x00000000000000000000000000000000000000a3"),
	}
	slots := []types.Hash{
		types.HexToHash("0x01"), types.HexToHash("0x42"), types.HexToHash("0xdead"),
	}

	// Exercise the paths that make a recycled object observable: several
	// accounts touched across several transactions, with reads that populate
	// the committed caches, writes that dirty them, and a Reset in between so
	// objects actually go back to the pool.
	run := func() map[string]uint64 {
		out := map[string]uint64{}
		sdb := New(slotReader{slots: map[types.Hash][]byte{slots[0]: {7}}})
		for round := 0; round < 3; round++ {
			for i, addr := range addrs {
				sdb.AddBalance(addr, uint256.NewInt(uint64(100+i)))
				sdb.SetNonce(addr, uint64(round*10+i))
				var got uint256.Int
				sdb.GetState(addr, &slots[i], &got)
				sdb.SetState(addr, &slots[i], *uint256.NewInt(uint64(round + 1)))
				if err := sdb.FinalizeTx(&params.Rules{}, NewNoopWriter()); err != nil {
					t.Fatalf("FinalizeTx: %v", err)
				}
			}
			// Returns every live object to the pool; the next round must not
			// see any of it.
			sdb.Reset()
		}
		for i, addr := range addrs {
			out[addr.Hex()+":nonce"] = sdb.GetNonce(addr)
			out[addr.Hex()+":balance"] = sdb.GetBalance(addr).Uint64()
			var v uint256.Int
			sdb.GetState(addr, &slots[i], &v)
			out[addr.Hex()+":slot"] = v.Uint64()
		}
		return out
	}

	var pooled, fresh map[string]uint64
	withPooling(t, true, func() { pooled = run() })
	withPooling(t, false, func() { fresh = run() })

	if !reflect.DeepEqual(pooled, fresh) {
		t.Fatalf("pooling changed observable state, so it cannot be used to "+
			"isolate a pooling bug:\n  pooled = %v\n  fresh  = %v", pooled, fresh)
	}
}

// With pooling off, putStateObject must not hand anything back, so a later
// newObject can never return a struct a previous account used.
func TestPoolingOffNeverRecycles(t *testing.T) {
	withPooling(t, false, func() {
		a := account.NewAccount()
		first := newObject(nil, types.HexToAddress("0x01"), &a, &a)
		putStateObject(first)
		second := newObject(nil, types.HexToAddress("0x02"), &a, &a)
		if first == second {
			t.Fatal("newObject returned the recycled struct with pooling disabled")
		}
	})
}

// And with pooling on it must, or the diagnostic has no baseline to contrast
// against.
func TestPoolingOnRecycles(t *testing.T) {
	withPooling(t, true, func() {
		a := account.NewAccount()
		// Drain whatever the pool happens to hold from earlier tests.
		for i := 0; i < 64; i++ {
			_ = newObject(nil, types.HexToAddress("0x00"), &a, &a)
		}
		first := newObject(nil, types.HexToAddress("0x01"), &a, &a)
		putStateObject(first)
		second := newObject(nil, types.HexToAddress("0x02"), &a, &a)
		if first != second {
			t.Skip("sync.Pool declined to return the struct (a GC in between is " +
				"allowed to drop it); the contrast test above is the load-bearing one")
		}
	})
}
