// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package parallel

import (
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// TestMVSCheckStore runs a presized transfer block with N42_MVS_CHECK on:
// checks 1, 2 and 4 find nothing; then one recipient credit is dropped
// from the store and check 2 reports exactly that write.
func TestMVSCheckStore(t *testing.T) {
	defer SetMVSCheckForTest(true)()
	defer SetPresizeForTest(true)()
	S, L, R := 1000, 10, 300
	base := &transferBase{accs: map[types.Address]*account.StateAccount{}}
	for s := 0; s < S; s++ {
		acc := &account.StateAccount{}
		acc.Balance.SetUint64(1 << 40)
		base.accs[transferAddr(1, s)] = acc
	}
	for r := 0; r < R; r++ {
		acc := &account.StateAccount{}
		acc.Balance.SetUint64(1000)
		base.accs[transferAddr(2, r)] = acc
	}
	recipient := func(i int) int { return (i * 31) % R }
	_, _, ex := runTransferBlock(t, base, S, L, R, 8, recipient)
	defer ex.Release()
	if ex.check == nil || ex.MVS().minSlots == 0 {
		t.Fatal("check state or presize not on")
	}
	rep := ex.CheckStore(42)
	if rep.Issues() != 0 || rep.Writes != 2*S*L || rep.Entries == 0 {
		t.Fatalf("clean block: %+v", rep)
	}
	// Drop tx 17's credit.
	var key LocationKey
	for _, wd := range ex.rwSets[17].Writes {
		if wd.Delta != nil {
			key = wd.Key
		}
	}
	ex.MVS().DropVersionForTest(key, 17)
	rep = ex.CheckStore(42)
	if rep.Lost != 1 || rep.Issues() != 1 {
		t.Fatalf("dropped credit: %+v", rep)
	}
	// A write stored in a foreign entry (check 4).
	other := NewMVS()
	ex.checkWriteEntry(&key, other.getOrCreateEntry(key), 17, true)
	if rep = ex.CheckStore(42); rep.Foreign != 1 {
		t.Fatalf("foreign entry: %+v", rep)
	}
}
