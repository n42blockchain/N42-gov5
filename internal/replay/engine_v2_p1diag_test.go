// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
)

func TestP1NewAccountShort(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	ibs := state.New(state.NewPlainStateReader(tx))
	from := types.HexToAddress("0x1000")
	fresh := types.HexToAddress("0x2000")
	funded := types.HexToAddress("0x3000")
	ibs.AddBalance(from, uint256.NewInt(1e18))
	ibs.AddBalance(funded, uint256.NewInt(1))

	mk := func(to *types.Address, gas uint64, value uint64) *transaction.Transaction {
		return transaction.NewTx(&transaction.LegacyTx{To: to, From: &from, Gas: gas, GasPrice: uint256.NewInt(1), Value: uint256.NewInt(value)})
	}
	cases := []struct {
		name           string
		tx             *transaction.Transaction
		newAcct, short bool
	}{
		{"21000 transfer to a fresh account", mk(&fresh, 21000, 5), true, true},
		{"enough gas for the state gas", mk(&fresh, 21000+183600, 5), true, false},
		{"one wei recipient is not empty", mk(&funded, 21000, 5), false, false},
		{"zero value", mk(&fresh, 21000, 0), false, false},
		{"self transfer", mk(&from, 21000, 5), false, false},
		{"creation", mk(nil, 100000, 5), false, false},
	}
	for _, c := range cases {
		n, s := p1NewAccountShort(ibs, c.tx, from)
		if n != c.newAcct || s != c.short {
			t.Errorf("%s: got newAccount=%v short=%v, want %v %v", c.name, n, s, c.newAcct, c.short)
		}
	}
}

func TestDiagCountersSnapshot(t *testing.T) {
	var d DiagCounters
	d.Add("b", 2)
	d.Add("a", 1)
	d.Add("b", 3)
	if got := d.Get("b"); got != 5 {
		t.Fatalf("b = %d, want 5", got)
	}
	if names := d.Names(); len(names) != 2 || names[0] != "a" {
		t.Fatalf("names = %v", names)
	}
	if snap := d.Snapshot(); snap["a"] != 1 || snap["b"] != 5 {
		t.Fatalf("snapshot = %v", snap)
	}
}
