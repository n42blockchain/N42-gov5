// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for state interfaces - verifying interface contracts and implementations.

package state

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// Interface Definition Tests
// =============================================================================

// TestStateReaderInterface verifies StateReader interface is properly defined
func TestStateReaderInterface(t *testing.T) {
	var _ StateReader = (*mockStateReader)(nil)
	t.Log("✓ StateReader interface properly defined")
}

// TestStateWriterInterface verifies StateWriter interface is properly defined
func TestStateWriterInterface(t *testing.T) {
	var _ StateWriter = (*mockStateWriter)(nil)
	t.Log("✓ StateWriter interface properly defined")
}

// TestWriterWithChangeSetsInterface verifies WriterWithChangeSets extends StateWriter
func TestWriterWithChangeSetsInterface(t *testing.T) {
	var _ WriterWithChangeSets = (*mockWriterWithChangeSets)(nil)
	
	// Verify it also satisfies StateWriter
	var wcw WriterWithChangeSets = &mockWriterWithChangeSets{}
	var _ StateWriter = wcw
	
	t.Log("✓ WriterWithChangeSets properly extends StateWriter")
}

// TestStateReaderWriterInterface verifies the combined interface
func TestStateReaderWriterInterface(t *testing.T) {
	var _ StateReaderWriter = (*mockStateReaderWriter)(nil)
	
	// Verify it satisfies both interfaces
	var srw StateReaderWriter = &mockStateReaderWriter{}
	var _ StateReader = srw
	var _ StateWriter = srw
	
	t.Log("✓ StateReaderWriter combines StateReader and StateWriter")
}

// =============================================================================
// Implementation Verification Tests
// =============================================================================

// TestPlainStateReaderImplementsInterface verifies PlainStateReader implements StateReader
func TestPlainStateReaderImplementsInterface(t *testing.T) {
	// This is a compile-time check - if it compiles, the implementation is correct
	var _ StateReader = (*PlainStateReader)(nil)
	t.Log("✓ PlainStateReader implements StateReader")
}

// TestPlainStateWriterImplementsInterface verifies PlainStateWriter implements WriterWithChangeSets
func TestPlainStateWriterImplementsInterface(t *testing.T) {
	var _ WriterWithChangeSets = (*PlainStateWriter)(nil)
	t.Log("✓ PlainStateWriter implements WriterWithChangeSets")
}

// TestHistoryStateReaderImplementsInterface verifies HistoryStateReader implements StateReader
func TestHistoryStateReaderImplementsInterface(t *testing.T) {
	var _ StateReader = (*HistoryStateReader)(nil)
	t.Log("✓ HistoryStateReader implements StateReader")
}

// TestNoopWriterImplementsInterface verifies NoopWriter implements StateWriter
func TestNoopWriterImplementsInterface(t *testing.T) {
	var _ StateWriter = (*NoopWriter)(nil)
	t.Log("✓ NoopWriter implements StateWriter")
}

// =============================================================================
// Method Signature Tests
// =============================================================================

// TestStateReaderMethods verifies all StateReader methods have correct signatures
func TestStateReaderMethods(t *testing.T) {
	var reader StateReader = &mockStateReader{}
	
	// ReadAccountData
	_, err := reader.ReadAccountData(types.Address{})
	_ = err
	t.Log("✓ ReadAccountData(address) (*account.StateAccount, error)")
	
	// ReadAccountStorage
	_, err = reader.ReadAccountStorage(types.Address{}, 0, &types.Hash{})
	_ = err
	t.Log("✓ ReadAccountStorage(address, incarnation, key) ([]byte, error)")
	
	// ReadAccountCode
	_, err = reader.ReadAccountCode(types.Address{}, 0, types.Hash{})
	_ = err
	t.Log("✓ ReadAccountCode(address, incarnation, codeHash) ([]byte, error)")
	
	// ReadAccountCodeSize
	_, err = reader.ReadAccountCodeSize(types.Address{}, 0, types.Hash{})
	_ = err
	t.Log("✓ ReadAccountCodeSize(address, incarnation, codeHash) (int, error)")
	
	// ReadAccountIncarnation
	_, err = reader.ReadAccountIncarnation(types.Address{})
	_ = err
	t.Log("✓ ReadAccountIncarnation(address) (uint16, error)")
}

// TestStateWriterMethods verifies all StateWriter methods have correct signatures
func TestStateWriterMethods(t *testing.T) {
	var writer StateWriter = &mockStateWriter{}
	
	// UpdateAccountData
	err := writer.UpdateAccountData(types.Address{}, nil, nil)
	_ = err
	t.Log("✓ UpdateAccountData(address, original, account) error")
	
	// UpdateAccountCode
	err = writer.UpdateAccountCode(types.Address{}, 0, types.Hash{}, nil)
	_ = err
	t.Log("✓ UpdateAccountCode(address, incarnation, codeHash, code) error")
	
	// DeleteAccount
	err = writer.DeleteAccount(types.Address{}, nil)
	_ = err
	t.Log("✓ DeleteAccount(address, original) error")
	
	// WriteAccountStorage
	err = writer.WriteAccountStorage(types.Address{}, 0, &types.Hash{}, nil, nil)
	_ = err
	t.Log("✓ WriteAccountStorage(address, incarnation, key, original, value) error")
	
	// CreateContract
	err = writer.CreateContract(types.Address{})
	_ = err
	t.Log("✓ CreateContract(address) error")
}

// =============================================================================
// Mock Implementations for Testing
// =============================================================================

type mockStateReader struct{}

func (m *mockStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	return nil, nil
}

func (m *mockStateReader) ReadAccountStorage(address types.Address, incarnation uint16, key *types.Hash) ([]byte, error) {
	return nil, nil
}

func (m *mockStateReader) ReadAccountCode(address types.Address, incarnation uint16, codeHash types.Hash) ([]byte, error) {
	return nil, nil
}

func (m *mockStateReader) ReadAccountCodeSize(address types.Address, incarnation uint16, codeHash types.Hash) (int, error) {
	return 0, nil
}

func (m *mockStateReader) ReadAccountIncarnation(address types.Address) (uint16, error) {
	return 0, nil
}

type mockStateWriter struct{}

func (m *mockStateWriter) UpdateAccountData(address types.Address, original, account *account.StateAccount) error {
	return nil
}

func (m *mockStateWriter) UpdateAccountCode(address types.Address, incarnation uint16, codeHash types.Hash, code []byte) error {
	return nil
}

func (m *mockStateWriter) DeleteAccount(address types.Address, original *account.StateAccount) error {
	return nil
}

func (m *mockStateWriter) WriteAccountStorage(address types.Address, incarnation uint16, key *types.Hash, original, value *uint256.Int) error {
	return nil
}

func (m *mockStateWriter) CreateContract(address types.Address) error {
	return nil
}

type mockWriterWithChangeSets struct {
	mockStateWriter
}

func (m *mockWriterWithChangeSets) WriteChangeSets() error {
	return nil
}

func (m *mockWriterWithChangeSets) WriteHistory() error {
	return nil
}

type mockStateReaderWriter struct {
	mockStateReader
	mockStateWriter
}

