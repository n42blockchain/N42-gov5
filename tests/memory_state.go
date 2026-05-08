// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Simple in-memory state backend for testing purposes.
// This implementation avoids MDBX resource issues on Windows.

package tests

import (
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// MemoryState is a simple in-memory state backend for testing
type MemoryState struct {
	accounts map[types.Address]*account.StateAccount
	code     map[types.Address][]byte
	storage  map[types.Address]map[types.Hash][]byte
}

// NewMemoryState creates a new in-memory state backend
func NewMemoryState() *MemoryState {
	return &MemoryState{
		accounts: make(map[types.Address]*account.StateAccount),
		code:     make(map[types.Address][]byte),
		storage:  make(map[types.Address]map[types.Hash][]byte),
	}
}

// Compile-time interface checks
var (
	_ state.StateReader = (*MemoryState)(nil)
	_ state.StateWriter = (*MemoryState)(nil)
)

// StateReader interface implementation

func (m *MemoryState) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	if acc, ok := m.accounts[address]; ok {
		return acc, nil
	}
	return nil, nil
}

func (m *MemoryState) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	if storage, ok := m.storage[address]; ok {
		if val, ok := storage[*key]; ok {
			return val, nil
		}
	}
	return nil, nil
}

func (m *MemoryState) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	if code, ok := m.code[address]; ok {
		return code, nil
	}
	return nil, nil
}

func (m *MemoryState) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	if code, ok := m.code[address]; ok {
		return len(code), nil
	}
	return 0, nil
}

// StateWriter interface implementation

func (m *MemoryState) UpdateAccountData(address types.Address, original, acc *account.StateAccount) error {
	if acc == nil {
		delete(m.accounts, address)
	} else {
		m.accounts[address] = acc
	}
	return nil
}

func (m *MemoryState) UpdateAccountCode(address types.Address, codeHash types.Hash, code []byte) error {
	m.code[address] = code
	return nil
}

func (m *MemoryState) DeleteAccount(address types.Address, original *account.StateAccount) error {
	delete(m.accounts, address)
	delete(m.code, address)
	delete(m.storage, address)
	return nil
}

func (m *MemoryState) WriteAccountStorage(address types.Address, key types.Hash, original, value uint256.Int) error {
	if m.storage[address] == nil {
		m.storage[address] = make(map[types.Hash][]byte)
	}
	if value.IsZero() {
		delete(m.storage[address], key)
	} else {
		m.storage[address][key] = value.Bytes()
	}
	return nil
}

func (m *MemoryState) CreateContract(address types.Address) error {
	return nil
}

// NoopWriter is a no-operation state writer for testing
type NoopWriter struct{}

func (n *NoopWriter) UpdateAccountData(address types.Address, original, account *account.StateAccount) error {
	return nil
}

func (n *NoopWriter) UpdateAccountCode(address types.Address, codeHash types.Hash, code []byte) error {
	return nil
}

func (n *NoopWriter) DeleteAccount(address types.Address, original *account.StateAccount) error {
	return nil
}

func (n *NoopWriter) WriteAccountStorage(address types.Address, key types.Hash, original, value uint256.Int) error {
	return nil
}

func (n *NoopWriter) CreateContract(address types.Address) error {
	return nil
}

