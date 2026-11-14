// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// unchangedReader serves a fixed set of existing accounts.
type unchangedReader struct {
	accounts map[types.Address]account.StateAccount
}

func (r unchangedReader) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	a, ok := r.accounts[addr]
	if !ok {
		return nil, nil
	}
	return &a, nil
}
func (unchangedReader) ReadAccountStorage(types.Address, *types.Hash) ([]byte, error) {
	return nil, nil
}
func (unchangedReader) ReadAccountCode(types.Address, types.Hash) ([]byte, error)  { return nil, nil }
func (unchangedReader) ReadAccountCodeSize(types.Address, types.Hash) (int, error) { return 0, nil }

// capturingRootComputer records the accounts a root was computed from.
type capturingRootComputer struct {
	accounts map[types.Address]*account.StateAccount
}

func (c *capturingRootComputer) ComputeRoot(accounts map[types.Address]*account.StateAccount, _ map[types.Address]map[types.Hash]*uint256.Int) (types.Hash, error) {
	c.accounts = accounts
	return types.Hash{}, nil
}

// TestUnchangedAccountNoOp pins UnchangedAccountNoOpTime (audit R3-E2): a
// zero credit to an account not yet loaded (the coinbase's zero tip, the
// recipient of a zero-value transfer) dirties it through the balanceInc
// path, and without the rule its unchanged leaf goes to the root computer;
// with it, only accounts that changed do. An account loaded before its zero
// credit is left out either way (stateObject.AddBalance of zero on a live
// non-empty object journals nothing): the path dependence the rule removes.
func TestUnchangedAccountNoOp(t *testing.T) {
	funded := func(balance uint64, nonce uint64) account.StateAccount {
		a := account.NewAccount()
		a.Initialised = true // as a stored account decodes
		a.Balance.SetUint64(balance)
		a.Nonce = nonce
		return a
	}
	coinbase := types.HexToAddress("0xc0")
	recipient := types.HexToAddress("0xc1")
	loaded := types.HexToAddress("0xc2")
	payer := types.HexToAddress("0xc3")
	payee := types.HexToAddress("0xc4")
	reader := unchangedReader{accounts: map[types.Address]account.StateAccount{
		coinbase:  funded(7, 0),
		recipient: funded(9, 1),
		loaded:    funded(3, 0),
		payer:     funded(100, 2),
		payee:     funded(1, 0),
	}}
	rules := &params.Rules{IsSpuriousDragon: true}
	for _, skip := range []bool{false, true} {
		ibs := New(reader)
		rc := &capturingRootComputer{}
		ibs.SetRootComputer(rc)
		ibs.SetSkipUnchangedAccounts(skip)
		_ = ibs.GetBalance(loaded) // read before its zero credit
		ibs.AddBalance(recipient, uint256.NewInt(0))
		ibs.AddBalance(loaded, uint256.NewInt(0))
		ibs.SubBalance(payer, uint256.NewInt(5))
		ibs.SetNonce(payer, 3)
		ibs.AddBalance(payee, uint256.NewInt(5))
		ibs.AddBalance(coinbase, uint256.NewInt(0))
		if err := ibs.FinalizeTx(rules, NewNoopWriter()); err != nil {
			t.Fatal(err)
		}
		ibs.IntermediateRoot()
		for addr, want := range map[types.Address]bool{coinbase: !skip, recipient: !skip, loaded: false, payer: true, payee: true} {
			if _, got := rc.accounts[addr]; got != want {
				t.Errorf("skip=%v: %x in the root's accounts = %v, want %v", skip, addr, got, want)
			}
		}
		if got := rc.accounts[payee]; got != nil && got.Balance.Uint64() != 6 {
			t.Errorf("skip=%v: payee balance %d, want 6", skip, got.Balance.Uint64())
		}
	}
}

// TestIsUnchangedAccountNoOp: unset is the old rule, a time activates it.
func TestIsUnchangedAccountNoOp(t *testing.T) {
	c := &params.ChainConfig{}
	if c.IsUnchangedAccountNoOp(1 << 40) {
		t.Fatal("unset activates")
	}
	c.UnchangedAccountNoOpTime = uint256.NewInt(100).ToBig()
	if c.IsUnchangedAccountNoOp(99) || !c.IsUnchangedAccountNoOp(100) {
		t.Fatal("activation time")
	}
}

// TestApplyUnchangedAccountRule: the helper every root path calls decides the
// flag exactly as ChainConfig.IsUnchangedAccountNoOp does at the block's time,
// off without a config or an activation time, and overrides an earlier value.
func TestApplyUnchangedAccountRule(t *testing.T) {
	ibs := New(unchangedReader{})
	ibs.SetSkipUnchangedAccounts(true)
	ibs.ApplyUnchangedAccountRule(nil, 1<<40)
	if ibs.SkipUnchangedAccounts() {
		t.Fatal("a nil config activates the rule")
	}
	unset := &params.ChainConfig{}
	set := &params.ChainConfig{UnchangedAccountNoOpTime: uint256.NewInt(100).ToBig()}
	for _, c := range []*params.ChainConfig{unset, set} {
		for _, time := range []uint64{0, 99, 100, 101, 1 << 40} {
			ibs.SetSkipUnchangedAccounts(!c.IsUnchangedAccountNoOp(time))
			ibs.ApplyUnchangedAccountRule(c, time)
			if got, want := ibs.SkipUnchangedAccounts(), c.IsUnchangedAccountNoOp(time); got != want {
				t.Errorf("time %d (activation %v): rule = %v, want %v", time, c.UnchangedAccountNoOpTime, got, want)
			}
		}
	}
	if ibs.ApplyUnchangedAccountRule(set, 99); ibs.SkipUnchangedAccounts() {
		t.Fatal("the block before the activation time takes the rule")
	}
	if ibs.ApplyUnchangedAccountRule(set, 100); !ibs.SkipUnchangedAccounts() {
		t.Fatal("the activation block does not take the rule")
	}
}
