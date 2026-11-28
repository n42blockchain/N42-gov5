// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/RoaringBitmap/roaring/roaring64"
	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func TestP1AllocDeltaPredeployRoundTrip(t *testing.T) {
	d := NewP1AllocDelta()
	c := types.HexToAddress("0xc0")
	slot := types.HexToHash("0x01")
	d.AddPrecreate(c, 4)
	d.AddPredeploy(c, []byte{0x60, 0x00}, map[types.Hash]types.Hash{slot: types.HexToHash("0x2a")}, 1, 5)
	path := filepath.Join(t.TempDir(), "delta.json")
	if err := WriteP1AllocDelta(path, d); err != nil {
		t.Fatal(err)
	}
	got, err := ReadP1AllocDelta(path)
	if err != nil {
		t.Fatal(err)
	}
	e := got.Entries[c]
	if e == nil || !e.Predeploy || !bytes.Equal(e.Code, []byte{0x60, 0x00}) || e.Nonce != 1 ||
		e.Storage[slot] != types.HexToHash("0x2a") || e.Wei.Uint64() != 1 || !e.Precreate || e.FirstBlock != 4 {
		t.Fatalf("entry = %+v", e)
	}
	if got.Predeploys() != 1 {
		t.Fatalf("predeploys = %d", got.Predeploys())
	}
}

func TestP1AllocDeltaVersions(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	v1 := write("v1.json", `{"version":1,"addresses":1,"precreates":1,"totalWei":"1","entries":[{"address":"0x00000000000000000000000000000000000000aa","wei":"1","precreate":true,"firstBlock":3}]}`)
	d, err := ReadP1AllocDelta(v1)
	if err != nil || len(d.Entries) != 1 || d.Predeploys() != 0 {
		t.Fatalf("v1 file: %v", err)
	}
	if _, err := ReadP1AllocDelta(write("v3.json", `{"version":3,"entries":[]}`)); err == nil {
		t.Fatal("version 3: want an error")
	}
	if _, err := ReadP1AllocDelta(write("v1p.json", `{"version":1,"entries":[{"address":"0x00000000000000000000000000000000000000aa","wei":"0","predeploy":true,"code":"0x00","firstBlock":3}]}`)); err == nil {
		t.Fatal("predeploy in a version 1 file: want an error")
	}
}

// legacyAccount encodes a in the chain-94 protobuf account form.
func legacyAccount(t *testing.T, a account.StateAccount) []byte {
	t.Helper()
	b, err := proto.Marshal(a.ToProtoMessage())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func bitmap(t *testing.T, blocks ...uint64) []byte {
	t.Helper()
	bm := roaring64.New()
	bm.AddMany(blocks)
	var buf bytes.Buffer
	if _, err := bm.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestP1SrcLegacyReader builds a chain-94 style source: block 10 creates
// contract c (nonce 0 -> 1, one storage slot, code via PlainCodeHash) and
// credits a fresh account r; block 12 changes the slot again.
func TestP1SrcLegacyReader(t *testing.T) {
	// The source's Storage table holds the legacy 2-byte-incarnation keys,
	// which the current table config's key conversion refuses on write:
	// open it as a plain DupSort table, as it is on disk.
	db := mdbx.NewMDBX(log2.New()).InMem(t.TempDir()).WithTableCfg(func(base kv.TableCfg) kv.TableCfg {
		cfg := kv.TableCfg{}
		for k, v := range base {
			cfg[k] = v
		}
		cfg[modules.Storage] = kv.TableCfgItem{Flags: kv.DupSort}
		cfg[p1SrcPlainCodeHash] = kv.TableCfgItem{}
		return cfg
	}).MustOpen()
	t.Cleanup(db.Close)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tx.Rollback)
	c := types.HexToAddress("0xc0")
	r := types.HexToAddress("0xd0")
	sender := types.HexToAddress("0xe0")
	slot := types.HexToHash("0x00")
	code := []byte{0x60, 0x01, 0x60, 0x00, 0x55}
	codeHash := crypto.Keccak256Hash(code)
	inc := []byte{0x00, 0x01}

	acc := func(nonce, bal uint64) account.StateAccount {
		a := account.NewAccount()
		a.Nonce = nonce
		a.Balance.SetUint64(bal)
		return a
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// Latest state: c (nonce 1), r (5 wei), sender (nonce 1).
	must(tx.Put(modules.Account, c[:], legacyAccount(t, acc(1, 0))))
	must(tx.Put(modules.Account, r[:], legacyAccount(t, acc(0, 5))))
	must(tx.Put(modules.Account, sender[:], legacyAccount(t, acc(1, 90))))
	// Block 10 account changes: c had 7 wei before (funded, no code), r and
	// sender did not exist.
	cs10 := modules.EncodeBlockNumber(10)
	must(tx.Put(modules.AccountChangeSet, cs10, append(bytes.Clone(c[:]), legacyAccount(t, acc(0, 7))...)))
	must(tx.Put(modules.AccountChangeSet, cs10, bytes.Clone(r[:])))
	must(tx.Put(modules.AccountChangeSet, cs10, append(bytes.Clone(sender[:]), legacyAccount(t, acc(0, 100))...)))
	for _, a := range []types.Address{c, r, sender} {
		must(tx.Put(modules.AccountsHistory, modules.AccountIndexChunkKey(a[:], ^uint64(0)), bitmap(t, 10)))
	}
	must(tx.Put(p1SrcPlainCodeHash, append(bytes.Clone(c[:]), 0, 1, 0, 0, 0, 0, 0, 0), codeHash[:]))
	must(tx.Put(modules.Code, codeHash[:], code))
	// Slot 0 of c: 0 before block 10, 0x2a after it, 0x2b after block 12.
	must(tx.Put(modules.StorageChangeSet, append(modules.EncodeBlockNumber(10), append(bytes.Clone(c[:]), inc...)...), bytes.Clone(slot[:])))
	must(tx.Put(modules.StorageChangeSet, append(modules.EncodeBlockNumber(12), append(bytes.Clone(c[:]), inc...)...), append(bytes.Clone(slot[:]), 0x2a)))
	must(tx.Put(modules.StorageHistory, modules.StorageIndexChunkKey(append(bytes.Clone(c[:]), slot[:]...), ^uint64(0)), bitmap(t, 10, 12)))
	// On disk: key = addr(20) inc(2) slot[:12], value = slot[12:] value.
	stoKey := append(append(bytes.Clone(c[:]), inc...), slot[:12]...)
	must(tx.Put(modules.Storage, stoKey, append(bytes.Clone(slot[12:]), 0x2b)))

	b, err := p1SrcReadBlock(tx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.accounts) != 3 {
		t.Fatalf("accounts = %d", len(b.accounts))
	}
	byAddr := map[types.Address]p1SrcAccount{}
	for _, a := range b.accounts {
		byAddr[a.addr] = a
	}
	if a := byAddr[c]; !a.created || a.preEmpty || a.postEmpty || a.postNonce != 1 {
		t.Fatalf("contract = %+v", a)
	}
	if a := byAddr[r]; a.created || !a.preEmpty || a.postEmpty {
		t.Fatalf("recipient = %+v", a)
	}
	if a := byAddr[sender]; a.created || a.preEmpty {
		t.Fatalf("sender = %+v", a)
	}
	ct, err := p1SrcReadContract(tx, c, 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ct.code, code) || ct.nonce != 1 || len(ct.storage) != 1 || ct.storage[slot] != types.HexToHash("0x2a") {
		t.Fatalf("contract after block 10 = %+v", ct)
	}
	// After block 12 the latest value applies.
	v, err := p1SrcStorageAsOf(tx, c, slot, 13)
	if err != nil || !bytes.Equal(v, []byte{0x2b}) {
		t.Fatalf("slot after block 12 = %x, %v", v, err)
	}
}

// TestP1DryRunReverts checks that a dry run reports the outcome and leaves
// no trace in the state.
func TestP1DryRunReverts(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	ibs := state.New(state.NewPlainStateReader(tx))
	from := types.HexToAddress("0x1000")
	to := types.HexToAddress("0x2000")
	ibs.AddBalance(from, uint256.NewInt(1e18))
	e := &EngineV2{cfg: ConfigV2{ChainConfig: params.TestChainConfig}}
	h := &block.Header{Number: uint256.NewInt(1), GasLimit: 30_000_000, Time: 1}
	gp := new(common.GasPool).AddGas(h.GasLimit)
	mk := func(gas uint64) *transaction.Transaction {
		return transaction.NewTx(&transaction.LegacyTx{To: &to, From: &from, Gas: gas, GasPrice: uint256.NewInt(1), Value: uint256.NewInt(5)})
	}
	bhf := func(uint64) types.Hash { return types.Hash{} }
	var seen uint64
	ok, err := e.p1DryRun(ibs, mk(21000), 0, types.Hash{}, h, gp, bhf, func() { seen = ibs.GetBalance(to).Uint64() })
	if err != nil || !ok || seen != 5 {
		t.Fatalf("dry run: ok=%v err=%v recipient=%d", ok, err, seen)
	}
	if ibs.GetBalance(to).Uint64() != 0 || ibs.GetNonce(from) != 0 || ibs.GetBalance(from).Uint64() != 1e18 || gp.Gas() != 30_000_000 {
		t.Fatal("dry run left state behind")
	}
	if _, err := e.p1DryRun(ibs, mk(20000), 0, types.Hash{}, h, gp, bhf, nil); err == nil {
		t.Fatal("intrinsic gas above the limit: want a rejection")
	}
}
