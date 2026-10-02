// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package snapshot

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// covMockWriter is a minimal state.WriterWithChangeSets recorder used to
// verify DiffCollector forwards every call unchanged.
type covMockWriter struct {
	updateAccountCalls   int
	updateCodeCalls      int
	deleteAccountCalls   int
	writeStorageCalls    int
	createContractCalls  int
	writeChangeSetsCalls int
	writeHistoryCalls    int
}

func (m *covMockWriter) UpdateAccountData(_ types.Address, _, _ *account.StateAccount) error {
	m.updateAccountCalls++
	return nil
}

func (m *covMockWriter) UpdateAccountCode(_ types.Address, _ types.Hash, _ []byte) error {
	m.updateCodeCalls++
	return nil
}

func (m *covMockWriter) DeleteAccount(_ types.Address, _ *account.StateAccount) error {
	m.deleteAccountCalls++
	return nil
}

func (m *covMockWriter) WriteAccountStorage(_ types.Address, _ types.Hash, _, _ uint256.Int) error {
	m.writeStorageCalls++
	return nil
}

func (m *covMockWriter) CreateContract(_ types.Address) error {
	m.createContractCalls++
	return nil
}

func (m *covMockWriter) WriteChangeSets() error {
	m.writeChangeSetsCalls++
	return nil
}

func (m *covMockWriter) WriteHistory() error {
	m.writeHistoryCalls++
	return nil
}

func TestDiffCollector_UpdateAccountData(t *testing.T) {
	inner := &covMockWriter{}
	dc := NewDiffCollector(inner)

	addr := testAddr(1)
	acc := testAccount(5)

	if err := dc.UpdateAccountData(addr, nil, acc); err != nil {
		t.Fatalf("UpdateAccountData: %v", err)
	}
	if inner.updateAccountCalls != 1 {
		t.Fatalf("expected inner.UpdateAccountData called once, got %d", inner.updateAccountCalls)
	}
	got, ok := dc.Accounts()[addr]
	if !ok {
		t.Fatalf("expected account to be recorded")
	}
	if got.Nonce != acc.Nonce {
		t.Fatalf("recorded account mismatch: got nonce %d want %d", got.Nonce, acc.Nonce)
	}
	// SelfCopy must produce a distinct object, not an alias.
	if got == acc {
		t.Fatalf("expected a copy, got the same pointer")
	}
}

func TestDiffCollector_UpdateAccountData_NilAccount(t *testing.T) {
	inner := &covMockWriter{}
	dc := NewDiffCollector(inner)
	addr := testAddr(2)

	if err := dc.UpdateAccountData(addr, nil, nil); err != nil {
		t.Fatalf("UpdateAccountData: %v", err)
	}
	if _, ok := dc.Accounts()[addr]; ok {
		t.Fatalf("expected no account recorded for nil acc")
	}
}

func TestDiffCollector_UpdateAccountCode(t *testing.T) {
	inner := &covMockWriter{}
	dc := NewDiffCollector(inner)

	if err := dc.UpdateAccountCode(testAddr(1), testHash(1), []byte("code")); err != nil {
		t.Fatalf("UpdateAccountCode: %v", err)
	}
	if inner.updateCodeCalls != 1 {
		t.Fatalf("expected inner.UpdateAccountCode called once, got %d", inner.updateCodeCalls)
	}
}

func TestDiffCollector_DeleteAccount(t *testing.T) {
	inner := &covMockWriter{}
	dc := NewDiffCollector(inner)
	addr := testAddr(3)
	acc := testAccount(1)

	// First record an update, then delete it — the delete must clear
	// the prior update from the accounts map.
	if err := dc.UpdateAccountData(addr, nil, acc); err != nil {
		t.Fatalf("UpdateAccountData: %v", err)
	}
	if err := dc.DeleteAccount(addr, acc); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if inner.deleteAccountCalls != 1 {
		t.Fatalf("expected inner.DeleteAccount called once, got %d", inner.deleteAccountCalls)
	}
	if _, ok := dc.Accounts()[addr]; ok {
		t.Fatalf("expected prior update to be cleared after delete")
	}
	if _, ok := dc.AccountDeletions()[addr]; !ok {
		t.Fatalf("expected deletion to be recorded")
	}
}

func TestDiffCollector_WriteAccountStorage_NonZero(t *testing.T) {
	inner := &covMockWriter{}
	dc := NewDiffCollector(inner)
	addr := testAddr(4)
	key := testHash(1)
	val := uint256.NewInt(42)

	if err := dc.WriteAccountStorage(addr, key, uint256.Int{}, *val); err != nil {
		t.Fatalf("WriteAccountStorage: %v", err)
	}
	if inner.writeStorageCalls != 1 {
		t.Fatalf("expected inner.WriteAccountStorage called once, got %d", inner.writeStorageCalls)
	}
	slots, ok := dc.Storage()[addr]
	if !ok {
		t.Fatalf("expected storage to be recorded for address")
	}
	stored, ok := slots[key]
	if !ok || stored == nil {
		t.Fatalf("expected non-nil stored value for non-zero write")
	}
}

func TestDiffCollector_WriteAccountStorage_Zero(t *testing.T) {
	inner := &covMockWriter{}
	dc := NewDiffCollector(inner)
	addr := testAddr(5)
	key := testHash(2)

	if err := dc.WriteAccountStorage(addr, key, *uint256.NewInt(1), uint256.Int{}); err != nil {
		t.Fatalf("WriteAccountStorage: %v", err)
	}
	slots, ok := dc.Storage()[addr]
	if !ok {
		t.Fatalf("expected storage map to be created for address")
	}
	stored, exists := slots[key]
	if !exists {
		t.Fatalf("expected key to be present (recorded as deletion)")
	}
	if stored != nil {
		t.Fatalf("expected nil value for zero write, got %v", stored)
	}
}

func TestDiffCollector_WriteAccountStorage_SecondSlotSameAddress(t *testing.T) {
	inner := &covMockWriter{}
	dc := NewDiffCollector(inner)
	addr := testAddr(6)

	if err := dc.WriteAccountStorage(addr, testHash(1), uint256.Int{}, *uint256.NewInt(1)); err != nil {
		t.Fatalf("WriteAccountStorage: %v", err)
	}
	if err := dc.WriteAccountStorage(addr, testHash(2), uint256.Int{}, *uint256.NewInt(2)); err != nil {
		t.Fatalf("WriteAccountStorage: %v", err)
	}
	slots := dc.Storage()[addr]
	if len(slots) != 2 {
		t.Fatalf("expected 2 slots recorded for address, got %d", len(slots))
	}
}

func TestDiffCollector_CreateContract(t *testing.T) {
	inner := &covMockWriter{}
	dc := NewDiffCollector(inner)
	if err := dc.CreateContract(testAddr(1)); err != nil {
		t.Fatalf("CreateContract: %v", err)
	}
	if inner.createContractCalls != 1 {
		t.Fatalf("expected inner.CreateContract called once, got %d", inner.createContractCalls)
	}
}

func TestDiffCollector_WriteChangeSetsAndHistory(t *testing.T) {
	inner := &covMockWriter{}
	dc := NewDiffCollector(inner)
	if err := dc.WriteChangeSets(); err != nil {
		t.Fatalf("WriteChangeSets: %v", err)
	}
	if err := dc.WriteHistory(); err != nil {
		t.Fatalf("WriteHistory: %v", err)
	}
	if inner.writeChangeSetsCalls != 1 {
		t.Fatalf("expected inner.WriteChangeSets called once, got %d", inner.writeChangeSetsCalls)
	}
	if inner.writeHistoryCalls != 1 {
		t.Fatalf("expected inner.WriteHistory called once, got %d", inner.writeHistoryCalls)
	}
}

func TestDiffCollector_AccountDeletionsEmptyInitially(t *testing.T) {
	inner := &covMockWriter{}
	dc := NewDiffCollector(inner)
	if len(dc.AccountDeletions()) != 0 {
		t.Fatalf("expected no deletions initially")
	}
	if len(dc.Accounts()) != 0 {
		t.Fatalf("expected no accounts initially")
	}
	if len(dc.Storage()) != 0 {
		t.Fatalf("expected no storage initially")
	}
}
