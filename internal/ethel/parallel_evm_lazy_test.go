// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// nopStateWriter records nothing; the skip-writer drops coinbase traffic
// before reaching it, and these tests only exercise that layer.
type nopStateWriter struct{}

func (nopStateWriter) UpdateAccountData(types.Address, *account.StateAccount, *account.StateAccount) error {
	return nil
}
func (nopStateWriter) UpdateAccountCode(types.Address, types.Hash, []byte) error { return nil }
func (nopStateWriter) DeleteAccount(types.Address, *account.StateAccount) error  { return nil }
func (nopStateWriter) WriteAccountStorage(types.Address, types.Hash, uint256.Int, uint256.Int) error {
	return nil
}
func (nopStateWriter) CreateContract(types.Address) error { return nil }

func acc(nonce uint64, bal uint64, code byte) *account.StateAccount {
	a := account.NewAccount()
	a.Initialised = true
	a.Nonce = nonce
	a.Balance.SetUint64(bal)
	if code != 0 {
		a.CodeHash = types.Hash{code}
	}
	return &a
}

// TestCoinbaseSkipWriter_TipCapture: pure balance increases accumulate as the
// deferred tip; nothing is marked unsound.
func TestCoinbaseSkipWriter_TipCapture(t *testing.T) {
	cb := types.Address{0xcb}
	w := &coinbaseSkipWriter{StateWriter: nopStateWriter{}, coinbase: cb}

	if err := w.UpdateAccountData(cb, acc(0, 100, 0), acc(0, 150, 0)); err != nil {
		t.Fatal(err)
	}
	if err := w.UpdateAccountData(cb, acc(0, 150, 0), acc(0, 175, 0)); err != nil {
		t.Fatal(err)
	}
	// Creation-by-tip (no original): also a pure credit.
	if err := w.UpdateAccountData(cb, nil, acc(0, 5, 0)); err != nil {
		t.Fatal(err)
	}
	if w.unsound {
		t.Fatal("pure credits flagged unsound")
	}
	if w.tip.Uint64() != 50+25+5 {
		t.Fatalf("tip = %s, want 80", w.tip.String())
	}
}

// TestCoinbaseSkipWriter_UnsoundDetection: every non-commutative coinbase
// mutation must flip the flag — nonce bump, code change, balance decrease,
// deletion, creation with a nonce.
func TestCoinbaseSkipWriter_UnsoundDetection(t *testing.T) {
	cb := types.Address{0xcb}
	cases := []struct {
		name string
		run  func(w *coinbaseSkipWriter)
	}{
		{"nonce bump", func(w *coinbaseSkipWriter) {
			_ = w.UpdateAccountData(cb, acc(1, 100, 0), acc(2, 110, 0))
		}},
		{"code change", func(w *coinbaseSkipWriter) {
			_ = w.UpdateAccountData(cb, acc(1, 100, 0), acc(1, 110, 7))
		}},
		{"balance decrease", func(w *coinbaseSkipWriter) {
			_ = w.UpdateAccountData(cb, acc(1, 100, 0), acc(1, 90, 0))
		}},
		{"deletion", func(w *coinbaseSkipWriter) {
			_ = w.DeleteAccount(cb, acc(1, 100, 0))
		}},
		{"created with nonce", func(w *coinbaseSkipWriter) {
			_ = w.UpdateAccountData(cb, nil, acc(1, 10, 0))
		}},
	}
	for _, tc := range cases {
		w := &coinbaseSkipWriter{StateWriter: nopStateWriter{}, coinbase: cb}
		tc.run(w)
		if !w.unsound {
			t.Errorf("%s: not flagged unsound", tc.name)
		}
	}

	// Non-coinbase traffic passes through untouched.
	w := &coinbaseSkipWriter{StateWriter: nopStateWriter{}, coinbase: cb}
	other := types.Address{0x01}
	if err := w.UpdateAccountData(other, acc(1, 100, 0), acc(2, 90, 7)); err != nil {
		t.Fatal(err)
	}
	if w.unsound || !w.tip.IsZero() {
		t.Fatal("non-coinbase update affected the skip-writer state")
	}
}
