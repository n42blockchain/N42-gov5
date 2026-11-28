package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/replay"
)

func TestNewFunderKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "funder.key")
	addr, err := newFunderKey(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", st.Mode().Perm())
	}
	key, err := crypto.LoadECDSA(path)
	if err != nil {
		t.Fatal(err)
	}
	if crypto.PubkeyToAddress(key.PublicKey) != addr {
		t.Error("address does not match the written key")
	}
	if _, err := newFunderKey(path); err == nil {
		t.Error("overwrote an existing key file")
	}
}

func TestPreludeFunder(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "funder.key")
	funder, err := newFunderKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	d1 := replay.NewP1AllocDelta()
	d1.AddPrecreate(types.Address{0x0a}, 1)
	d1.AddPrecreate(types.Address{0x0b}, 2)
	d2 := replay.NewP1AllocDelta()
	d2.AddTopup(types.Address{0x0b}, uint256.NewInt(1000), 3)
	p1, p2 := filepath.Join(dir, "d1.json"), filepath.Join(dir, "d2.json")
	if err := replay.WriteP1AllocDelta(p1, d1); err != nil {
		t.Fatal(err)
	}
	if err := replay.WriteP1AllocDelta(p2, d2); err != nil {
		t.Fatal(err)
	}
	baseFee := uint256.NewInt(1e9)
	addr, bal, n, err := preludeFunder([]string{p1, p2}, keyPath, baseFee)
	if err != nil {
		t.Fatal(err)
	}
	if addr != funder || n != 2 {
		t.Fatalf("funder %s n %d", addr.Hex(), n)
	}
	want := replay.P1PreludeFunderBalance(replay.P1PreludeTransfers(d1, d2), replay.P1PreludeGasPerTx(), replay.P1PreludeMaxFee(baseFee))
	if bal.String() != want.Dec() {
		t.Errorf("balance %s, want %s", bal, want.Dec())
	}
	// sum 1002 wei + 2 * gas * 2 gwei * 1.1
	gas := new(uint256.Int).Mul(uint256.NewInt(2*replay.P1PreludeGasPerTx()), uint256.NewInt(2e9))
	gas.Add(gas, new(uint256.Int).Div(gas, uint256.NewInt(10)))
	if want.Cmp(gas.AddUint64(gas, 1002)) != 0 {
		t.Errorf("balance %s, want sum + budget %s", want.Dec(), gas.Dec())
	}

	// The funder may not be a delta address.
	d3 := replay.NewP1AllocDelta()
	d3.AddPrecreate(funder, 1)
	p3 := filepath.Join(dir, "d3.json")
	if err := replay.WriteP1AllocDelta(p3, d3); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := preludeFunder([]string{p3}, keyPath, baseFee); err == nil {
		t.Error("funder in the delta: want an error")
	}

	alloc := conf.GenesisAlloc{funder: {Balance: "0x10"}}
	if err := addBalance(alloc, funder, bal); err != nil {
		t.Fatal(err)
	}
	if got := alloc[funder].Balance; got != new(uint256.Int).AddUint64(want, 16).Dec() {
		t.Errorf("alloc balance %s", got)
	}
}
