// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// profile1RuleConfigs returns a Profile-1 chain config and a chain outside
// the profile with the same two write-set rules active from time 0.
func profile1RuleConfigs() (profile1, outside *params.ChainConfig) {
	return &params.ChainConfig{N42Profile: params.N42Profile1},
		&params.ChainConfig{UnchangedAccountNoOpTime: new(big.Int), StorageNetChangeTime: new(big.Int)}
}

// D3: on Profile 1 the system address 0xff..fe is held to the no-op rule:
// touched but unchanged, it is not written. Outside the profile it keeps its
// exemption and is written whenever it is dirty.
func TestProfile1SystemAddressNoOp(t *testing.T) {
	sys := account.NewAccount()
	sys.Initialised = true
	sys.Balance.SetUint64(1)
	reader := unchangedReader{accounts: map[types.Address]account.StateAccount{SystemAddress: sys}}
	profile1, outside := profile1RuleConfigs()
	rules := &params.Rules{IsSpuriousDragon: true}
	for _, tc := range []struct {
		name    string
		config  *params.ChainConfig
		written bool
	}{
		{"profile1", profile1, false},
		{"outside profile", outside, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ibs := New(reader)
			rc := &capturingRootComputer{}
			ibs.SetRootComputer(rc)
			ibs.ApplyUnchangedAccountRule(tc.config, 1)
			ibs.AddBalance(SystemAddress, uint256.NewInt(0)) // touched, unchanged
			if err := ibs.FinalizeTx(rules, NewNoopWriter()); err != nil {
				t.Fatal(err)
			}
			ibs.IntermediateRoot()
			if _, got := rc.accounts[SystemAddress]; got != tc.written {
				t.Fatalf("0xff..fe in the root's accounts = %v, want %v", got, tc.written)
			}
		})
	}
}

// D3: a changed system address is written on Profile 1 too.
func TestProfile1SystemAddressChangedIsWritten(t *testing.T) {
	profile1, _ := profile1RuleConfigs()
	ibs := New(unchangedReader{})
	rc := &capturingRootComputer{}
	ibs.SetRootComputer(rc)
	ibs.ApplyUnchangedAccountRule(profile1, 1)
	ibs.AddBalance(SystemAddress, uint256.NewInt(3))
	if err := ibs.FinalizeTx(&params.Rules{IsSpuriousDragon: true}, NewNoopWriter()); err != nil {
		t.Fatal(err)
	}
	ibs.IntermediateRoot()
	if a := rc.accounts[SystemAddress]; a == nil || a.Balance.Uint64() != 3 {
		t.Fatalf("changed 0xff..fe in the root's accounts = %v, want balance 3", a)
	}
}

// D5: on Profile 1 a slot whose pre-block value was never captured (first
// stored by the transaction that created its contract) is compared with the
// value read from the state: set and reset to zero in the creation, it is
// not written. Outside the profile it is kept (a delete of an absent leaf).
func TestProfile1StorageNetChangeReadsUncapturedPreValue(t *testing.T) {
	fresh := types.HexToAddress("0xa6")
	s0, s1 := types.Hash{}, types.HexToHash("0x01")
	profile1, outside := profile1RuleConfigs()
	rules := &params.Rules{IsSpuriousDragon: true}
	for _, tc := range []struct {
		name string
		cfg  *params.ChainConfig
		kept bool
	}{
		{"profile1", profile1, false},
		{"outside profile", outside, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ibs := New(netChangeReader{storage: map[types.Address]map[types.Hash]uint64{}})
			rc := &netChangeRootComputer{}
			ibs.SetRootComputer(rc)
			ibs.ApplyUnchangedAccountRule(tc.cfg, 1)
			ibs.CreateAccount(fresh, true)
			ibs.SetNonce(fresh, 1)
			ibs.SetState(fresh, &s0, *uint256.NewInt(5))
			ibs.SetState(fresh, &s0, *uint256.NewInt(0))
			ibs.SetState(fresh, &s1, *uint256.NewInt(1))
			if err := ibs.FinalizeTx(rules, NewNoopWriter()); err != nil {
				t.Fatal(err)
			}
			ibs.IntermediateRoot()
			if _, got := rc.storage[fresh][s0]; got != tc.kept {
				t.Fatalf("set-and-reset slot in the root's storage = %v, want %v", got, tc.kept)
			}
			if v := rc.storage[fresh][s1]; v == nil || v.Uint64() != 1 {
				t.Fatalf("written slot = %v, want 1", v)
			}
		})
	}
}

// D5 on a contract destroyed and created again in the block: on Profile 1
// each slot is compared with the value the state held at the start of the
// block, so a slot that ends at its pre-block value is not written and one
// that ends at zero from a non-zero value is deleted. Outside the profile
// every stored slot is written.
func TestProfile1StorageNetChangeWipedContract(t *testing.T) {
	recreated := types.HexToAddress("0xb1")
	s0, s1 := types.Hash{}, types.HexToHash("0x01")
	acc := account.NewAccount()
	acc.Initialised = true
	acc.Nonce = 1
	acc.CodeHash = types.HexToHash("0xc0de")
	reader := netChangeReader{
		unchangedReader: unchangedReader{accounts: map[types.Address]account.StateAccount{recreated: acc}},
		storage:         map[types.Address]map[types.Hash]uint64{recreated: {s0: 7, s1: 7}},
	}
	profile1, outside := profile1RuleConfigs()
	rules := &params.Rules{IsSpuriousDragon: true}
	for _, tc := range []struct {
		name     string
		cfg      *params.ChainConfig
		restored bool // s1 (back to its pre-block 7) in the write set
	}{
		{"profile1", profile1, false},
		{"outside profile", outside, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ibs := New(reader)
			rc := &netChangeRootComputer{}
			ibs.SetRootComputer(rc)
			ibs.ApplyUnchangedAccountRule(tc.cfg, 1)
			ibs.Selfdestruct(recreated)
			if err := ibs.FinalizeTx(rules, NewNoopWriter()); err != nil {
				t.Fatal(err)
			}
			ibs.CreateAccount(recreated, true)
			ibs.SetNonce(recreated, 1)
			ibs.SetState(recreated, &s0, *uint256.NewInt(1))
			ibs.SetState(recreated, &s0, *uint256.NewInt(0))
			ibs.SetState(recreated, &s1, *uint256.NewInt(7))
			if err := ibs.FinalizeTx(rules, NewNoopWriter()); err != nil {
				t.Fatal(err)
			}
			ibs.IntermediateRoot()
			if v, ok := rc.storage[recreated][s0]; !ok || v == nil || !v.IsZero() {
				t.Fatalf("slot 7 -> 0 = %v (present %v), want a delete", v, ok)
			}
			if _, got := rc.storage[recreated][s1]; got != tc.restored {
				t.Fatalf("slot 7 -> 7 in the root's storage = %v, want %v", got, tc.restored)
			}
		})
	}
}
