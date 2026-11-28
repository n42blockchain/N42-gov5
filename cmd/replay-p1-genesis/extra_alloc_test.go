package main

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/replay"
)

func TestAddAllocDelta(t *testing.T) {
	held := types.HexToAddress("0x01")
	hexBal := types.HexToAddress("0x02")
	fresh := types.HexToAddress("0x03")
	alloc := conf.GenesisAlloc{
		held:   {Balance: "1000"},
		hexBal: {Balance: "0x10", Nonce: 2},
	}
	d := replay.NewP1AllocDelta()
	d.AddTopup(held, uint256.NewInt(5), 1)
	d.AddTopup(hexBal, uint256.NewInt(4), 1)
	d.AddPrecreate(fresh, 9)
	if err := addAllocDelta(alloc, d); err != nil {
		t.Fatal(err)
	}
	for a, want := range map[types.Address]string{held: "1005", hexBal: "20", fresh: "1"} {
		if got := alloc[a].Balance; got != want {
			t.Errorf("%s balance %s, want %s", a.Hex(), got, want)
		}
	}
	if alloc[hexBal].Nonce != 2 {
		t.Errorf("nonce lost")
	}
	if countPrecreates(d) != 1 {
		t.Errorf("precreates = %d", countPrecreates(d))
	}
	bad := conf.GenesisAlloc{held: {Balance: "xyz"}}
	if err := addAllocDelta(bad, d); err == nil {
		t.Error("bad balance: want an error")
	}
}
