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

func TestSplitBalanceOnly(t *testing.T) {
	thr, err := parseThresholdWei("100")
	if err != nil || thr.String() != "100000000000000000000" {
		t.Fatalf("threshold %v %v", thr, err)
	}
	if v, _ := parseThresholdWei("0.5"); v.String() != "500000000000000000" {
		t.Errorf("0.5 N = %s", v)
	}
	big100 := "100000000000000000000"
	alloc := conf.GenesisAlloc{
		{0x01}: {Balance: "5"},                                       // moved
		{0x02}: {Balance: big100},                                    // kept (== threshold)
		{0x03}: {Balance: "0"},                                       // deleted
		{0x04}: {Balance: ""},                                        // deleted
		{0x05}: {Balance: "1", Nonce: 1},                             // kept: nonce
		{0x06}: {Balance: "1", Code: []byte{0x60}},                   // kept: code
		{0x07}: {Balance: "1", Storage: map[types.Hash]types.Hash{{1}: {2}}}, // kept: storage
		{0x08}: {Balance: "0", Nonce: 1},                             // kept: nonce, zero
		{0x09}: {Balance: "0x10"},                                    // moved (hex)
	}
	moved, zeros, kept, err := splitBalanceOnly(alloc, thr)
	if err != nil {
		t.Fatal(err)
	}
	if zeros != 2 || kept != 1 || len(moved.Entries) != 2 {
		t.Fatalf("zeros %d kept %d moved %d", zeros, kept, len(moved.Entries))
	}
	if moved.Entries[types.Address{0x01}].Wei.Uint64() != 5 || moved.Entries[types.Address{0x09}].Wei.Uint64() != 16 {
		t.Error("moved amounts")
	}
	if len(alloc) != 5 {
		t.Errorf("alloc %d, want 5 left", len(alloc))
	}
	for _, a := range []byte{0x02, 0x05, 0x06, 0x07, 0x08} {
		if _, ok := alloc[types.Address{a}]; !ok {
			t.Errorf("account %x removed", a)
		}
	}

	// The funder budget covers the moved accounts too.
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "funder.key")
	if _, err := newFunderKey(keyPath); err != nil {
		t.Fatal(err)
	}
	mp := filepath.Join(dir, "moved.json")
	if err := replay.WriteP1AllocDelta(mp, moved); err != nil {
		t.Fatal(err)
	}
	d1 := replay.NewP1AllocDelta()
	d1.AddPrecreate(types.Address{0x01}, 1) // also in the moved file: summed
	d1.AddPrecreate(types.Address{0x0a}, 1)
	p1 := filepath.Join(dir, "d1.json")
	if err := replay.WriteP1AllocDelta(p1, d1); err != nil {
		t.Fatal(err)
	}
	baseFee := uint256.NewInt(1e9)
	_, bal, n, err := preludeFunder([]string{p1, mp}, keyPath, baseFee)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("transfers %d, want 3 (0x01 summed)", n)
	}
	want := replay.P1PreludeFunderBalance(replay.P1PreludeTransfers(d1, moved), replay.P1PreludeGasPerTx(), replay.P1PreludeMaxFee(baseFee))
	if bal.String() != want.Dec() {
		t.Errorf("balance %s, want %s", bal, want.Dec())
	}
	// Order of the files does not matter.
	_, bal2, _, _ := preludeFunder([]string{mp, p1}, keyPath, baseFee)
	if bal2.Cmp(bal) != 0 {
		t.Error("file order changes the budget")
	}
}
