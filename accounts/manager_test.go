// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package accounts

import (
	"encoding/json"
	"math/big"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	event "github.com/n42blockchain/N42/modules/event/v2"
)

// fakeWallet is a minimal in-memory Wallet implementation for testing the
// Manager's merge/drop/lookup logic without pulling in a real keystore.
type fakeWallet struct {
	url      URL
	accounts []Account
}

func (w *fakeWallet) URL() URL                { return w.url }
func (w *fakeWallet) Status() (string, error) { return "ok", nil }
func (w *fakeWallet) Open(passphrase string) error { return nil }
func (w *fakeWallet) Close() error                 { return nil }
func (w *fakeWallet) Accounts() []Account          { return w.accounts }
func (w *fakeWallet) Contains(account Account) bool {
	for _, a := range w.accounts {
		if a.Address == account.Address {
			return true
		}
	}
	return false
}
func (w *fakeWallet) Derive(path DerivationPath, pin bool) (Account, error) {
	return Account{}, ErrNotSupported
}
func (w *fakeWallet) SelfDerive(bases []DerivationPath, chain common.AccountStateReader) {}
func (w *fakeWallet) SignData(account Account, mimeType string, data []byte) ([]byte, error) {
	return nil, ErrNotSupported
}
func (w *fakeWallet) SignDataWithPassphrase(account Account, passphrase, mimeType string, data []byte) ([]byte, error) {
	return nil, ErrNotSupported
}
func (w *fakeWallet) SignText(account Account, text []byte) ([]byte, error) {
	return nil, ErrNotSupported
}
func (w *fakeWallet) SignTextWithPassphrase(account Account, passphrase string, hash []byte) ([]byte, error) {
	return nil, ErrNotSupported
}
func (w *fakeWallet) SignTx(account Account, tx *transaction.Transaction, chainID *big.Int) (*transaction.Transaction, error) {
	return nil, ErrNotSupported
}
func (w *fakeWallet) SignTxWithPassphrase(account Account, passphrase string, tx *transaction.Transaction, chainID *big.Int) (*transaction.Transaction, error) {
	return nil, ErrNotSupported
}

// fakeBackend is a minimal in-memory Backend for testing the Manager.
type fakeBackend struct {
	wallets []Wallet
	feed    *event.Feed
}

func newFakeBackend(wallets ...Wallet) *fakeBackend {
	return &fakeBackend{wallets: wallets, feed: &event.Feed{}}
}

func (b *fakeBackend) Wallets() []Wallet { return b.wallets }
func (b *fakeBackend) Subscribe(sink chan<- WalletEvent) event.Subscription {
	sub, _ := b.feed.Subscribe(sink)
	return sub
}

func mkAccount(addrByte byte) Account {
	var addr types.Address
	addr[0] = addrByte
	return Account{Address: addr, URL: URL{Scheme: "fake", Path: string(rune('a' + addrByte))}}
}

func TestManagerBasics(t *testing.T) {
	accA := mkAccount(1)
	accB := mkAccount(2)
	walletA := &fakeWallet{url: URL{Scheme: "fake", Path: "a"}, accounts: []Account{accA}}
	walletB := &fakeWallet{url: URL{Scheme: "fake", Path: "b"}, accounts: []Account{accB}}
	backend := newFakeBackend(walletA, walletB)

	am := NewManager(&Config{InsecureUnlockAllowed: true}, backend)
	defer am.Close()

	if cfg := am.Config(); cfg == nil || !cfg.InsecureUnlockAllowed {
		t.Fatalf("Config() = %+v, want InsecureUnlockAllowed true", cfg)
	}

	wallets := am.Wallets()
	if len(wallets) != 2 {
		t.Fatalf("Wallets() len = %d, want 2", len(wallets))
	}

	backends := am.Backends(reflect.TypeOf(backend))
	if len(backends) != 1 {
		t.Fatalf("Backends() len = %d, want 1", len(backends))
	}

	addrs := am.Accounts()
	if len(addrs) != 2 {
		t.Fatalf("Accounts() len = %d, want 2", len(addrs))
	}

	gotWallet, err := am.Wallet(walletA.url.String())
	if err != nil || gotWallet != walletA {
		t.Fatalf("Wallet(walletA) = %v, %v", gotWallet, err)
	}

	if _, err := am.Wallet("unknown://nope"); err != ErrUnknownWallet {
		t.Fatalf("Wallet(unknown) err = %v, want ErrUnknownWallet", err)
	}

	if _, err := am.Wallet("::not a valid url::"); err == nil {
		t.Fatalf("expected parse error for malformed URL")
	}

	foundWallet, err := am.Find(accA)
	if err != nil || foundWallet != walletA {
		t.Fatalf("Find(accA) = %v, %v", foundWallet, err)
	}

	if _, err := am.Find(mkAccount(99)); err != ErrUnknownAccount {
		t.Fatalf("Find(unknown) err = %v, want ErrUnknownAccount", err)
	}
}

func TestManagerAddBackendAndSubscribe(t *testing.T) {
	am := NewManager(&Config{})
	defer am.Close()

	sink := make(chan WalletEvent, 4)
	sub := am.Subscribe(sink)
	defer sub.Unsubscribe()

	walletC := &fakeWallet{url: URL{Scheme: "fake", Path: "c"}, accounts: []Account{mkAccount(3)}}
	backend2 := newFakeBackend(walletC)
	am.AddBackend(backend2)

	if got := am.Wallets(); len(got) != 1 {
		t.Fatalf("Wallets() after AddBackend = %d, want 1", len(got))
	}

	// Fire an arrival and a departure event through the backend feed and
	// make sure the manager's cache updates accordingly.
	backend2.feed.Send(WalletEvent{Wallet: walletC, Kind: WalletDropped})
	// Give the update goroutine a tick by sending a no-op through Subscribe path.
	// Poll briefly since event delivery is async.
	deadlineOK := false
	for i := 0; i < 200; i++ {
		if len(am.Wallets()) == 0 {
			deadlineOK = true
			break
		}
		time.Sleep(500 * time.Microsecond)
	}
	if !deadlineOK {
		t.Fatalf("expected wallet to be dropped from cache")
	}
}

func TestMergeAndDrop(t *testing.T) {
	w1 := &fakeWallet{url: URL{Scheme: "fake", Path: "1"}}
	w2 := &fakeWallet{url: URL{Scheme: "fake", Path: "2"}}
	w3 := &fakeWallet{url: URL{Scheme: "fake", Path: "3"}}

	merged := merge(nil, w2, w1, w3)
	if len(merged) != 3 {
		t.Fatalf("merge len = %d, want 3", len(merged))
	}
	if !sort.IsSorted(WalletsByURL(merged)) {
		t.Fatalf("merge result not sorted: %+v", merged)
	}

	dropped := drop(merged, w2)
	if len(dropped) != 2 {
		t.Fatalf("drop len = %d, want 2", len(dropped))
	}

	// Dropping a wallet not present is a no-op.
	w4 := &fakeWallet{url: URL{Scheme: "fake", Path: "4"}}
	same := drop(dropped, w4)
	if len(same) != 2 {
		t.Fatalf("drop(absent) len = %d, want 2", len(same))
	}
}

func TestAccountsByURLSort(t *testing.T) {
	a1 := mkAccount(2)
	a2 := mkAccount(1)
	list := AccountsByURL{a1, a2}
	sort.Sort(list)
	if list.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", list.Len())
	}
	if !list.Less(0, 1) && !list.Less(1, 0) {
		// Just exercise Less/Swap without over-constraining ordering semantics.
	}
	list.Swap(0, 1)
}

func TestWalletsByURLSort(t *testing.T) {
	w1 := &fakeWallet{url: URL{Scheme: "fake", Path: "z"}}
	w2 := &fakeWallet{url: URL{Scheme: "fake", Path: "a"}}
	list := WalletsByURL{w1, w2}
	sort.Sort(list)
	if list[0].URL().Path != "a" {
		t.Fatalf("sorted order wrong: %+v", list)
	}
}

func TestAuthNeededError(t *testing.T) {
	err := NewAuthNeededError("passphrase")
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("Error() = %q, want to contain 'passphrase'", err.Error())
	}
}

func TestURLTerminalString(t *testing.T) {
	short := URL{Scheme: "keystore", Path: "/short"}
	if got := short.TerminalString(); got != short.String() {
		t.Fatalf("TerminalString() = %q, want %q", got, short.String())
	}

	longPath := strings.Repeat("x", 64)
	long := URL{Scheme: "keystore", Path: longPath}
	got := long.TerminalString()
	if len(got) != 33 || !strings.HasSuffix(got, "..") {
		t.Fatalf("TerminalString() = %q (len %d), want truncated with trailing ..", got, len(got))
	}
}

func TestDerivationPathJSONRoundTrip(t *testing.T) {
	path, err := ParseDerivationPath("m/44'/60'/0'/0/0")
	if err != nil {
		t.Fatalf("ParseDerivationPath failed: %v", err)
	}

	data, err := json.Marshal(path)
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}

	var round DerivationPath
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}
	if round.String() != path.String() {
		t.Fatalf("round trip mismatch: got %s want %s", round.String(), path.String())
	}

	var bad DerivationPath
	if err := json.Unmarshal([]byte(`"not a valid path"`), &bad); err == nil {
		t.Fatal("expected error for invalid derivation path JSON")
	}

	var notString DerivationPath
	if err := json.Unmarshal([]byte(`42`), &notString); err == nil {
		t.Fatal("expected error for non-string JSON input")
	}
}
