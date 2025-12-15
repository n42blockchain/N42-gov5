// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for instrumented StateReader/StateWriter wrappers.

package state

import (
	"sync"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// InstrumentedReader Tests
// =============================================================================

func TestInstrumentedReaderImplementsInterface(t *testing.T) {
	var _ StateReader = (*InstrumentedReader)(nil)
	t.Log("✓ InstrumentedReader implements StateReader")
}

func TestInstrumentedReaderDisabled(t *testing.T) {
	mock := &mockStateReader{}
	reader := NewInstrumentedReader(mock, false)

	// Should pass through without instrumentation
	_, _ = reader.ReadAccountData(types.Address{})
	_, _ = reader.ReadAccountStorage(types.Address{}, 0, &types.Hash{})
	_, _ = reader.ReadAccountCode(types.Address{}, 0, types.Hash{})
	_, _ = reader.ReadAccountCodeSize(types.Address{}, 0, types.Hash{})
	_, _ = reader.ReadAccountIncarnation(types.Address{})

	stats := reader.Stats()
	if stats.TotalReads() != 0 {
		t.Errorf("Expected 0 reads when disabled, got %d", stats.TotalReads())
	}
	t.Log("✓ InstrumentedReader disabled mode works correctly")
}

func TestInstrumentedReaderEnabled(t *testing.T) {
	mock := &mockStateReader{}
	reader := NewInstrumentedReader(mock, true)

	// Perform various reads
	_, _ = reader.ReadAccountData(types.Address{})
	_, _ = reader.ReadAccountData(types.Address{})
	_, _ = reader.ReadAccountStorage(types.Address{}, 0, &types.Hash{})
	_, _ = reader.ReadAccountCode(types.Address{}, 0, types.Hash{})
	_, _ = reader.ReadAccountCodeSize(types.Address{}, 0, types.Hash{})
	_, _ = reader.ReadAccountIncarnation(types.Address{})

	stats := reader.Stats()
	if stats.ReadAccountCount != 2 {
		t.Errorf("Expected 2 account reads, got %d", stats.ReadAccountCount)
	}
	if stats.ReadStorageCount != 1 {
		t.Errorf("Expected 1 storage read, got %d", stats.ReadStorageCount)
	}
	if stats.TotalReads() != 6 {
		t.Errorf("Expected 6 total reads, got %d", stats.TotalReads())
	}
	if stats.TotalTime() == 0 {
		t.Error("Expected non-zero total time")
	}
	t.Log("✓ InstrumentedReader enabled mode counts correctly")
}

func TestInstrumentedReaderReset(t *testing.T) {
	mock := &mockStateReader{}
	reader := NewInstrumentedReader(mock, true)

	_, _ = reader.ReadAccountData(types.Address{})
	reader.Reset()

	stats := reader.Stats()
	if stats.TotalReads() != 0 {
		t.Errorf("Expected 0 reads after reset, got %d", stats.TotalReads())
	}
	t.Log("✓ InstrumentedReader reset works correctly")
}

func TestInstrumentedReaderConcurrentAccess(t *testing.T) {
	mock := &mockStateReader{}
	reader := NewInstrumentedReader(mock, true)

	var wg sync.WaitGroup
	numGoroutines := 10
	readsPerGoroutine := 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < readsPerGoroutine; j++ {
				_, _ = reader.ReadAccountData(types.Address{})
				_, _ = reader.ReadAccountStorage(types.Address{}, 0, &types.Hash{})
			}
		}()
	}
	wg.Wait()

	stats := reader.Stats()
	expectedReads := uint64(numGoroutines * readsPerGoroutine * 2)
	if stats.TotalReads() != expectedReads {
		t.Errorf("Expected %d reads, got %d", expectedReads, stats.TotalReads())
	}
	t.Log("✓ InstrumentedReader handles concurrent access correctly")
}

// =============================================================================
// InstrumentedWriter Tests
// =============================================================================

func TestInstrumentedWriterImplementsInterface(t *testing.T) {
	var _ StateWriter = (*InstrumentedWriter)(nil)
	t.Log("✓ InstrumentedWriter implements StateWriter")
}

func TestInstrumentedWriterDisabled(t *testing.T) {
	mock := &mockStateWriter{}
	writer := NewInstrumentedWriter(mock, false)

	// Should pass through without instrumentation
	_ = writer.UpdateAccountData(types.Address{}, nil, nil)
	_ = writer.UpdateAccountCode(types.Address{}, 0, types.Hash{}, nil)
	_ = writer.DeleteAccount(types.Address{}, nil)
	_ = writer.WriteAccountStorage(types.Address{}, 0, &types.Hash{}, nil, nil)
	_ = writer.CreateContract(types.Address{})

	stats := writer.Stats()
	if stats.TotalWrites() != 0 {
		t.Errorf("Expected 0 writes when disabled, got %d", stats.TotalWrites())
	}
	t.Log("✓ InstrumentedWriter disabled mode works correctly")
}

func TestInstrumentedWriterEnabled(t *testing.T) {
	mock := &mockStateWriter{}
	writer := NewInstrumentedWriter(mock, true)

	// Perform various writes
	_ = writer.UpdateAccountData(types.Address{}, nil, nil)
	_ = writer.UpdateAccountData(types.Address{}, nil, nil)
	_ = writer.UpdateAccountCode(types.Address{}, 0, types.Hash{}, nil)
	_ = writer.DeleteAccount(types.Address{}, nil)
	_ = writer.WriteAccountStorage(types.Address{}, 0, &types.Hash{}, nil, nil)
	_ = writer.CreateContract(types.Address{})

	stats := writer.Stats()
	if stats.UpdateAccountCount != 2 {
		t.Errorf("Expected 2 account updates, got %d", stats.UpdateAccountCount)
	}
	if stats.TotalWrites() != 6 {
		t.Errorf("Expected 6 total writes, got %d", stats.TotalWrites())
	}
	if stats.TotalTime() == 0 {
		t.Error("Expected non-zero total time")
	}
	t.Log("✓ InstrumentedWriter enabled mode counts correctly")
}

func TestInstrumentedWriterConcurrentAccess(t *testing.T) {
	mock := &mockStateWriter{}
	writer := NewInstrumentedWriter(mock, true)

	var wg sync.WaitGroup
	numGoroutines := 10
	writesPerGoroutine := 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < writesPerGoroutine; j++ {
				_ = writer.UpdateAccountData(types.Address{}, nil, nil)
				_ = writer.WriteAccountStorage(types.Address{}, 0, &types.Hash{}, nil, nil)
			}
		}()
	}
	wg.Wait()

	stats := writer.Stats()
	expectedWrites := uint64(numGoroutines * writesPerGoroutine * 2)
	if stats.TotalWrites() != expectedWrites {
		t.Errorf("Expected %d writes, got %d", expectedWrites, stats.TotalWrites())
	}
	t.Log("✓ InstrumentedWriter handles concurrent access correctly")
}

// =============================================================================
// Stats Tests
// =============================================================================

func TestReaderStatsTotalReads(t *testing.T) {
	stats := ReaderStats{
		ReadAccountCount:  10,
		ReadStorageCount:  20,
		ReadCodeCount:     5,
		ReadCodeSizeCount: 3,
		ReadIncarnCount:   2,
	}
	if stats.TotalReads() != 40 {
		t.Errorf("Expected 40 total reads, got %d", stats.TotalReads())
	}
	t.Log("✓ ReaderStats.TotalReads works correctly")
}

func TestReaderStatsTotalTime(t *testing.T) {
	stats := ReaderStats{
		ReadAccountTime: 100 * time.Millisecond,
		ReadStorageTime: 200 * time.Millisecond,
		ReadCodeTime:    50 * time.Millisecond,
	}
	expected := 350 * time.Millisecond
	if stats.TotalTime() != expected {
		t.Errorf("Expected %v total time, got %v", expected, stats.TotalTime())
	}
	t.Log("✓ ReaderStats.TotalTime works correctly")
}

func TestWriterStatsTotalWrites(t *testing.T) {
	stats := WriterStats{
		UpdateAccountCount:  10,
		UpdateCodeCount:     5,
		DeleteAccountCount:  2,
		WriteStorageCount:   20,
		CreateContractCount: 3,
	}
	if stats.TotalWrites() != 40 {
		t.Errorf("Expected 40 total writes, got %d", stats.TotalWrites())
	}
	t.Log("✓ WriterStats.TotalWrites works correctly")
}

// =============================================================================
// Round-trip Tests for RLP encoding (PR 2.1 verification)
// =============================================================================

func TestRLPRoundTripAccount(t *testing.T) {
	// This test verifies that RLP encoding/decoding works correctly
	// after the package move from internal/avm/rlp to common/rlp

	original := &account.StateAccount{
		Nonce:       42,
		Incarnation: 1,
	}
	original.Balance = *uint256.NewInt(1000000)

	// Encode
	buffer := make([]byte, original.EncodingLengthForStorage())
	original.EncodeForStorage(buffer)

	// Decode
	decoded := &account.StateAccount{}
	if err := decoded.DecodeForStorage(buffer); err != nil {
		t.Fatalf("Failed to decode account: %v", err)
	}

	// Verify
	if decoded.Nonce != original.Nonce {
		t.Errorf("Nonce mismatch: got %d, want %d", decoded.Nonce, original.Nonce)
	}
	if decoded.Balance.Cmp(&original.Balance) != 0 {
		t.Errorf("Balance mismatch: got %v, want %v", &decoded.Balance, &original.Balance)
	}

	t.Log("✓ RLP round-trip for Account works correctly")
}

// =============================================================================
// Interface Integration Tests
// =============================================================================

func TestInstrumentedReaderPassesThrough(t *testing.T) {
	// Verify that the instrumented reader correctly passes data through
	expectedAccount := &account.StateAccount{Nonce: 123}
	mock := &mockStateReaderWithData{account: expectedAccount}
	reader := NewInstrumentedReader(mock, true)

	result, err := reader.ReadAccountData(types.Address{})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result == nil || result.Nonce != 123 {
		t.Errorf("Expected account with nonce 123, got %v", result)
	}
	t.Log("✓ InstrumentedReader passes data through correctly")
}

// =============================================================================
// Mock implementations
// =============================================================================

type mockStateReaderWithData struct {
	account *account.StateAccount
}

func (m *mockStateReaderWithData) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	return m.account, nil
}

func (m *mockStateReaderWithData) ReadAccountStorage(address types.Address, incarnation uint16, key *types.Hash) ([]byte, error) {
	return nil, nil
}

func (m *mockStateReaderWithData) ReadAccountCode(address types.Address, incarnation uint16, codeHash types.Hash) ([]byte, error) {
	return nil, nil
}

func (m *mockStateReaderWithData) ReadAccountCodeSize(address types.Address, incarnation uint16, codeHash types.Hash) (int, error) {
	return 0, nil
}

func (m *mockStateReaderWithData) ReadAccountIncarnation(address types.Address) (uint16, error) {
	return 0, nil
}

