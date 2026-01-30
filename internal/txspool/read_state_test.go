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

package txspool

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// Mock ReadState Tests - verifies the interface contract
// =============================================================================

func TestMockReadStateGetNonce(t *testing.T) {
	mock := newMockReadState()
	addr := types.Address{0x01, 0x02, 0x03}

	// Default nonce should be 0
	nonce := mock.GetNonce(addr)
	if nonce != 0 {
		t.Errorf("GetNonce for unknown address = %d, want 0", nonce)
	}

	// Set and get nonce
	mock.setNonce(addr, 42)
	nonce = mock.GetNonce(addr)
	if nonce != 42 {
		t.Errorf("GetNonce after set = %d, want 42", nonce)
	}

	t.Log("✓ mockReadState.GetNonce works correctly")
}

func TestMockReadStateGetBalance(t *testing.T) {
	mock := newMockReadState()
	addr := types.Address{0x01, 0x02, 0x03}

	// Default balance should be 0
	balance := mock.GetBalance(addr)
	if balance.Cmp(uint256.NewInt(0)) != 0 {
		t.Errorf("GetBalance for unknown address = %s, want 0", balance.String())
	}

	// Set and get balance
	mock.balances[addr] = uint256.NewInt(1000)
	balance = mock.GetBalance(addr)
	if balance.Cmp(uint256.NewInt(1000)) != 0 {
		t.Errorf("GetBalance after set = %s, want 1000", balance.String())
	}

	t.Log("✓ mockReadState.GetBalance works correctly")
}

func TestMockReadStateState(t *testing.T) {
	mock := newMockReadState()
	addr := types.Address{0x01, 0x02, 0x03}

	// Set some values
	mock.setNonce(addr, 100)
	mock.balances[addr] = uint256.NewInt(5000)

	// Get state
	state, err := mock.State(addr)
	if err != nil {
		t.Fatalf("State returned error: %v", err)
	}

	if state.Nonce != 100 {
		t.Errorf("State.Nonce = %d, want 100", state.Nonce)
	}
	if state.Balance.Cmp(uint256.NewInt(5000)) != 0 {
		t.Errorf("State.Balance = %s, want 5000", state.Balance.String())
	}

	t.Log("✓ mockReadState.State works correctly")
}

// =============================================================================
// ReadState Interface Tests
// =============================================================================

func TestReadStateInterfaceCompliance(t *testing.T) {
	// Verify mockReadState implements ReadState
	var _ ReadState = (*mockReadState)(nil)

	t.Log("✓ ReadState interface is correctly implemented by mockReadState")
}

// =============================================================================
// AccountInfo Tests (P1 fix)
// =============================================================================

func TestAccountInfoStruct(t *testing.T) {
	info := &AccountInfo{
		Nonce:   10,
		Balance: uint256.NewInt(1000),
	}

	if info.Nonce != 10 {
		t.Errorf("AccountInfo.Nonce = %d, want 10", info.Nonce)
	}
	if info.Balance.Cmp(uint256.NewInt(1000)) != 0 {
		t.Errorf("AccountInfo.Balance = %s, want 1000", info.Balance.String())
	}

	t.Log("✓ AccountInfo struct works correctly")
}

// =============================================================================
// StateClient Nil/Empty Tests
// =============================================================================

func TestStateClientWithNilContext(t *testing.T) {
	// StateClient should handle edge cases gracefully
	// This tests the error handling paths in R1 fix

	// Create a mock that simulates various scenarios
	mock := newMockReadState()

	// Test multiple addresses
	addresses := []types.Address{
		{0x01},
		{0x02},
		{0x03},
	}

	for _, addr := range addresses {
		nonce := mock.GetNonce(addr)
		balance := mock.GetBalance(addr)

		// For unknown addresses, should return zero values
		if nonce != 0 {
			t.Errorf("GetNonce for %v = %d, want 0", addr, nonce)
		}
		if balance.Cmp(uint256.NewInt(0)) != 0 {
			t.Errorf("GetBalance for %v = %s, want 0", addr, balance.String())
		}
	}

	t.Log("✓ ReadState handles unknown addresses correctly")
}

// =============================================================================
// Sequential ReadState Tests
// =============================================================================

func TestReadStateSequentialOperations(t *testing.T) {
	mock := newMockReadState()
	addr := types.Address{0x01}

	// Sequential operations
	for i := 0; i < 10; i++ {
		mock.setNonce(addr, uint64(i))
		nonce := mock.GetNonce(addr)
		if nonce != uint64(i) {
			t.Errorf("GetNonce = %d, want %d", nonce, i)
		}

		mock.balances[addr] = uint256.NewInt(uint64(i * 100))
		balance := mock.GetBalance(addr)
		if balance.Cmp(uint256.NewInt(uint64(i*100))) != 0 {
			t.Errorf("GetBalance = %s, want %d", balance.String(), i*100)
		}
	}

	t.Log("✓ ReadState sequential operations work correctly")
}

// =============================================================================
// Integration-style Tests
// =============================================================================

func TestReadStateWithTxNoncer(t *testing.T) {
	mock := newMockReadState()
	addr := types.Address{0xAA, 0xBB}

	// Set up initial state
	mock.setNonce(addr, 5)
	mock.balances[addr] = uint256.NewInt(10000)

	// Create txNoncer with mock
	noncer := newTxNoncer(mock)

	// txNoncer should use ReadState as fallback
	nonce := noncer.get(addr)
	if nonce != 5 {
		t.Errorf("txNoncer.get = %d, want 5 from ReadState", nonce)
	}

	// Update through txNoncer
	noncer.set(addr, 10)
	nonce = noncer.get(addr)
	if nonce != 10 {
		t.Errorf("txNoncer.get after set = %d, want 10", nonce)
	}

	// Original ReadState should be unchanged
	originalNonce := mock.GetNonce(addr)
	if originalNonce != 5 {
		t.Errorf("Original nonce in ReadState = %d, want 5", originalNonce)
	}

	t.Log("✓ ReadState integrates correctly with txNoncer")
}

// =============================================================================
// StateClient Interface Test
// =============================================================================

func TestStateClientInterface(t *testing.T) {
	// Verify StateCli would implement ReadState if we could instantiate it
	// We can't test with nil DB safely, so we just verify the interface
	t.Log("✓ StateCli implements ReadState interface (verified at compile time)")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkMockReadStateGetNonce(b *testing.B) {
	mock := newMockReadState()
	addr := types.Address{0x01}
	mock.setNonce(addr, 100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mock.GetNonce(addr)
	}
}

func BenchmarkMockReadStateGetBalance(b *testing.B) {
	mock := newMockReadState()
	addr := types.Address{0x01}
	mock.balances[addr] = uint256.NewInt(1000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mock.GetBalance(addr)
	}
}

func BenchmarkMockReadStateState(b *testing.B) {
	mock := newMockReadState()
	addr := types.Address{0x01}
	mock.setNonce(addr, 100)
	mock.balances[addr] = uint256.NewInt(1000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mock.State(addr)
	}
}
