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

// netChangeReader serves existing accounts and their stored slots.
type netChangeReader struct {
	unchangedReader
	storage map[types.Address]map[types.Hash]uint64
}

func (r netChangeReader) ReadAccountStorage(addr types.Address, key *types.Hash) ([]byte, error) {
	v, ok := r.storage[addr][*key]
	if !ok || v == 0 {
		return nil, nil
	}
	return uint256.NewInt(v).Bytes(), nil
}

// netChangeRootComputer records the accounts and slots a root was computed from.
type netChangeRootComputer struct {
	accounts map[types.Address]*account.StateAccount
	storage  map[types.Address]map[types.Hash]*uint256.Int
}

func (c *netChangeRootComputer) ComputeRoot(accounts map[types.Address]*account.StateAccount, storage map[types.Address]map[types.Hash]*uint256.Int) (types.Hash, error) {
	c.accounts, c.storage = accounts, storage
	return types.Hash{}, nil
}

// TestStorageNetChange pins StorageNetChangeTime: a slot whose value at the
// end of the block is its value at the start writes no leaf, whether it was
// restored inside one transaction (a reentrancy guard) or by a later one, and
// a contract with no other change then counts as unchanged. Without the rule
// every slot the block stored to is written with its final value.
func TestStorageNetChange(t *testing.T) {
	contract := func() account.StateAccount {
		a := account.NewAccount()
		a.Initialised = true
		a.Nonce = 1
		a.CodeHash = types.HexToHash("0xc0de")
		return a
	}
	guard := types.HexToAddress("0xa1")   // slot 0: 0 -> 1 -> 0 in one tx
	nonZero := types.HexToAddress("0xa2") // slot 0: 7 -> 9 -> 7 in one tx
	twoTx := types.HexToAddress("0xa3")   // slot 0: 7 -> 9 in tx 1, -> 7 in tx 2
	mixed := types.HexToAddress("0xa4")   // slot 0 restored, slot 1 changed
	cleared := types.HexToAddress("0xa5") // slot 0: 7 -> 0
	fresh := types.HexToAddress("0xa6")   // created this block, slot 0: 0 -> 1 -> 0 (start never captured: kept)
	s0, s1 := types.Hash{}, types.HexToHash("0x01")
	reader := netChangeReader{
		unchangedReader: unchangedReader{accounts: map[types.Address]account.StateAccount{
			guard: contract(), nonZero: contract(), twoTx: contract(), mixed: contract(), cleared: contract(),
		}},
		storage: map[types.Address]map[types.Hash]uint64{
			nonZero: {s0: 7}, twoTx: {s0: 7}, mixed: {s0: 7, s1: 3}, cleared: {s0: 7},
		},
	}
	rules := &params.Rules{IsSpuriousDragon: true}
	set := func(ibs *IntraBlockState, addr types.Address, key types.Hash, v uint64) {
		ibs.SetState(addr, &key, *uint256.NewInt(v))
	}
	for _, on := range []bool{false, true} {
		ibs := New(reader)
		rc := &netChangeRootComputer{}
		ibs.SetRootComputer(rc)
		ibs.SetSkipUnchangedAccounts(true)
		ibs.SetStorageNetChange(on)

		set(ibs, guard, s0, 1)
		set(ibs, guard, s0, 0)
		set(ibs, nonZero, s0, 9)
		set(ibs, nonZero, s0, 7)
		set(ibs, twoTx, s0, 9)
		set(ibs, mixed, s0, 9)
		set(ibs, mixed, s0, 7)
		set(ibs, mixed, s1, 4)
		set(ibs, cleared, s0, 0)
		ibs.CreateAccount(fresh, true)
		ibs.SetNonce(fresh, 1)
		set(ibs, fresh, s0, 1)
		set(ibs, fresh, s0, 0)
		if err := ibs.FinalizeTx(rules, NewNoopWriter()); err != nil {
			t.Fatal(err)
		}
		set(ibs, twoTx, s0, 7)
		if err := ibs.FinalizeTx(rules, NewNoopWriter()); err != nil {
			t.Fatal(err)
		}
		ibs.IntermediateRoot()

		for addr, want := range map[types.Address]bool{guard: !on, nonZero: !on, twoTx: !on, mixed: true, cleared: true, fresh: true} {
			if _, got := rc.accounts[addr]; got != want {
				t.Errorf("rule=%v: %x in the root's accounts = %v, want %v", on, addr, got, want)
			}
		}
		type slot struct {
			addr types.Address
			key  types.Hash
		}
		for sl, want := range map[slot]bool{
			{guard, s0}: !on, {nonZero, s0}: !on, {twoTx, s0}: !on, {mixed, s0}: !on,
			{mixed, s1}: true, {cleared, s0}: true, {fresh, s0}: true,
		} {
			if _, got := rc.storage[sl.addr][sl.key]; got != want {
				t.Errorf("rule=%v: %x slot %x in the root's storage = %v, want %v", on, sl.addr, sl.key, got, want)
			}
		}
		if v := rc.storage[mixed][s1]; v == nil || v.Uint64() != 4 {
			t.Errorf("rule=%v: changed slot value %v, want 4", on, v)
		}
		if v := rc.storage[cleared][s0]; v == nil || !v.IsZero() {
			t.Errorf("rule=%v: cleared slot value %v, want 0", on, v)
		}
	}
}

// TestApplyStorageNetChangeRule: the helper every root path calls sets the
// rule from ChainConfig.StorageNetChangeTime, independently of the
// unchanged-account rule.
func TestApplyStorageNetChangeRule(t *testing.T) {
	c := &params.ChainConfig{StorageNetChangeTime: uint256.NewInt(100).ToBig()}
	ibs := New(unchangedReader{})
	for time, want := range map[uint64]bool{0: false, 99: false, 100: true, 1 << 40: true} {
		ibs.ApplyUnchangedAccountRule(c, time)
		if got := ibs.StorageNetChange(); got != want {
			t.Errorf("time %d: rule = %v, want %v", time, got, want)
		}
		if ibs.SkipUnchangedAccounts() {
			t.Errorf("time %d: the unchanged-account rule follows the storage rule", time)
		}
	}
	ibs.SetStorageNetChange(true)
	if ibs.ApplyUnchangedAccountRule(nil, 1<<40); ibs.StorageNetChange() {
		t.Fatal("a nil config activates the rule")
	}
	if ibs.ApplyUnchangedAccountRule(&params.ChainConfig{}, 1<<40); ibs.StorageNetChange() {
		t.Fatal("an unset time activates the rule")
	}
}

// TestStorageNetChangeKeepsWipedContractSlots: a contract destroyed and
// created again in the block reads zero for every slot from then on, so the
// starting value captured for a slot after the wipe is zero and not what the
// tree holds. A slot the new contract sets and resets to zero must still be
// written (as a delete): the wipe capture may not list it (a reader that
// cannot enumerate storage), and the old leaf would stay in the tree.
func TestStorageNetChangeKeepsWipedContractSlots(t *testing.T) {
	recreated := types.HexToAddress("0xb1")
	s0 := types.Hash{}
	acc := account.NewAccount()
	acc.Initialised = true
	acc.Nonce = 1
	acc.CodeHash = types.HexToHash("0xc0de")
	reader := netChangeReader{
		unchangedReader: unchangedReader{accounts: map[types.Address]account.StateAccount{recreated: acc}},
		storage:         map[types.Address]map[types.Hash]uint64{recreated: {s0: 7}},
	}
	rules := &params.Rules{IsSpuriousDragon: true}
	for _, on := range []bool{false, true} {
		ibs := New(reader)
		rc := &netChangeRootComputer{}
		ibs.SetRootComputer(rc)
		ibs.SetSkipUnchangedAccounts(true)
		ibs.SetStorageNetChange(on)

		ibs.Selfdestruct(recreated)
		if err := ibs.FinalizeTx(rules, NewNoopWriter()); err != nil {
			t.Fatal(err)
		}
		ibs.CreateAccount(recreated, true)
		ibs.SetNonce(recreated, 1)
		if err := ibs.FinalizeTx(rules, NewNoopWriter()); err != nil {
			t.Fatal(err)
		}
		ibs.SetState(recreated, &s0, *uint256.NewInt(1))
		ibs.SetState(recreated, &s0, *uint256.NewInt(0))
		if err := ibs.FinalizeTx(rules, NewNoopWriter()); err != nil {
			t.Fatal(err)
		}
		ibs.IntermediateRoot()

		v, ok := rc.storage[recreated][s0]
		if !ok || v == nil || !v.IsZero() {
			t.Errorf("rule=%v: wiped contract's slot in the root's storage = %v (present %v), want a zero write", on, v, ok)
		}
	}
}
