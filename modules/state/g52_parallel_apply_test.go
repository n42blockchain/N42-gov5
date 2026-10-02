// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

func mustKeccak(b []byte) []byte {
	h := crypto.Keccak256Hash(b)
	return h[:]
}

// TestG52IntraBlockStateAccountPrefetchGetter pins the SetAccountPrefetch /
// AccountPrefetch recorder pair: nil before Set, and exactly what was set
// afterwards.
func TestG52IntraBlockStateAccountPrefetchGetter(t *testing.T) {
	ibs, _ := g51NewTestIBS(t)
	if ibs.AccountPrefetch() != nil {
		t.Fatal("fresh IBS should report a nil AccountPrefetch")
	}
	pf := NewAccountPrefetch(nil)
	ibs.SetAccountPrefetch(pf)
	if ibs.AccountPrefetch() != pf {
		t.Fatal("AccountPrefetch() should return exactly what SetAccountPrefetch recorded")
	}
}

// TestG52ApplyBlockCommitToIBS replays a BlockCommit with one new-contract
// account (code + balance + nonce), one storage write, a wiped/recreated
// account, and a coinbase tip delta, verifying each of
// ApplyBlockCommitToIBS's five phases lands correctly. No other test in
// the suite drives this bridge.
func TestG52ApplyBlockCommitToIBS(t *testing.T) {
	db := memdb.NewTestDB(t)
	ctx := context.Background()

	contract := types.HexToAddress("0x00000000000000000000000000000000000000f9")
	code := []byte{0x60, 0x01, 0x60, 0x02, 0x01}
	codeHash := types.BytesToHash(mustKeccak(code))

	wiped := types.HexToAddress("0x00000000000000000000000000000000000000fa")
	slot := types.HexToHash("0x01")
	coinbase := types.HexToAddress("0x00000000000000000000000000000000000000fb")

	// Seed the wiped account as pre-existing so Selfdestruct + CreateAccount
	// has something real to wipe/recreate.
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		orig := account.NewAccount()
		pre := account.NewAccount()
		pre.Initialised = true
		pre.Nonce = 1
		pre.Balance = *uint256.NewInt(5)
		w := NewPlainStateWriter(tx, tx, 1)
		return w.UpdateAccountData(wiped, &orig, &pre)
	}); err != nil {
		t.Fatal(err)
	}

	acctEnc := func(nonce uint64, balance uint64, ch types.Hash) []byte {
		a := account.NewAccount()
		a.Initialised = true
		a.Nonce = nonce
		a.Balance = *uint256.NewInt(balance)
		a.CodeHash = ch
		buf := make([]byte, a.EncodingLengthForStorage())
		a.EncodeForStorage(buf)
		return buf
	}

	bc := &BlockCommit{
		CoinbaseAddress: coinbase,
		CoinbaseDelta:   uint256.NewInt(7),
		Writes: []CommitEntry{
			{Key: EncodeCodeKey(codeHash), Value: code},
			{Key: EncodeWipeKey(wiped), Value: []byte{1}},
			{Key: EncodeAccountKey(contract), Value: acctEnc(3, 100, codeHash)},
			{Key: EncodeAccountKey(wiped), Value: acctEnc(1, 9, emptyCodeHashH)},
			{Key: EncodeStorageKey(contract, slot), Value: uint256.NewInt(42).Bytes()},
		},
	}

	if err := db.View(ctx, func(tx kv.Tx) error {
		ibs := New(NewPlainStateReader(tx))
		if err := ApplyBlockCommitToIBS(bc, ibs); err != nil {
			t.Fatalf("ApplyBlockCommitToIBS: %v", err)
		}

		if got := ibs.GetBalance(contract); got.Uint64() != 100 {
			t.Fatalf("contract balance = %d, want 100", got.Uint64())
		}
		if got := ibs.GetNonce(contract); got != 3 {
			t.Fatalf("contract nonce = %d, want 3", got)
		}
		if got := ibs.GetCode(contract); string(got) != string(code) {
			t.Fatalf("contract code = %x, want %x", got, code)
		}
		var val uint256.Int
		ibs.GetState(contract, &slot, &val)
		if val.Uint64() != 42 {
			t.Fatalf("contract slot = %d, want 42", val.Uint64())
		}
		if got := ibs.GetBalance(wiped); got.Uint64() != 9 {
			t.Fatalf("wiped/recreated account balance = %d, want 9", got.Uint64())
		}
		if got := ibs.GetBalance(coinbase); got.Uint64() != 7 {
			t.Fatalf("coinbase balance = %d, want 7 (delta applied to a zero base)", got.Uint64())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
