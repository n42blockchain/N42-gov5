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
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// Mock ReadState for Testing
// =============================================================================

type mockReadState struct {
	nonces   map[types.Address]uint64
	balances map[types.Address]*uint256.Int
}

func newMockReadState() *mockReadState {
	return &mockReadState{
		nonces:   make(map[types.Address]uint64),
		balances: make(map[types.Address]*uint256.Int),
	}
}

func (m *mockReadState) GetNonce(addr types.Address) uint64 {
	if nonce, ok := m.nonces[addr]; ok {
		return nonce
	}
	return 0
}

func (m *mockReadState) GetBalance(addr types.Address) *uint256.Int {
	if balance, ok := m.balances[addr]; ok {
		return balance
	}
	return uint256.NewInt(0)
}

func (m *mockReadState) State(addr types.Address) (*account.StateAccount, error) {
	return &account.StateAccount{
		Nonce:   m.GetNonce(addr),
		Balance: *m.GetBalance(addr),
	}, nil
}

func (m *mockReadState) setNonce(addr types.Address, nonce uint64) {
	m.nonces[addr] = nonce
}

// =============================================================================
// txNoncer Tests
// =============================================================================

func TestNewTxNoncer(t *testing.T) {
	db := newMockReadState()
	noncer := newTxNoncer(db)

	if noncer == nil {
		t.Fatal("newTxNoncer should not return nil")
	}
	if noncer.fallback == nil {
		t.Error("fallback not set correctly")
	}
	if noncer.nonces == nil {
		t.Error("nonces map should be initialized")
	}

	t.Logf("✓ newTxNoncer works correctly")
}

func TestTxNoncerGet(t *testing.T) {
	db := newMockReadState()
	addr := types.Address{0x01}
	db.setNonce(addr, 100)

	noncer := newTxNoncer(db)

	// First get should fetch from fallback
	nonce := noncer.get(addr)
	if nonce != 100 {
		t.Errorf("get() = %d, want 100", nonce)
	}

	// Second get should use cached value
	nonce2 := noncer.get(addr)
	if nonce2 != 100 {
		t.Errorf("get() = %d, want 100", nonce2)
	}

	t.Logf("✓ txNoncer.get works correctly")
}

func TestTxNoncerSet(t *testing.T) {
	db := newMockReadState()
	noncer := newTxNoncer(db)
	addr := types.Address{0x01}

	noncer.set(addr, 50)

	if noncer.get(addr) != 50 {
		t.Errorf("get() = %d, want 50 after set", noncer.get(addr))
	}

	t.Logf("✓ txNoncer.set works correctly")
}

func TestTxNoncerSetIfLower(t *testing.T) {
	db := newMockReadState()
	addr := types.Address{0x01}
	db.setNonce(addr, 100)

	noncer := newTxNoncer(db)

	// setIfLower with higher value should not change
	noncer.setIfLower(addr, 150)
	if noncer.get(addr) != 100 {
		t.Errorf("setIfLower with higher value should not change, got %d", noncer.get(addr))
	}

	// setIfLower with lower value should change
	noncer.setIfLower(addr, 50)
	if noncer.get(addr) != 50 {
		t.Errorf("setIfLower with lower value should change, got %d", noncer.get(addr))
	}

	t.Logf("✓ txNoncer.setIfLower works correctly")
}

func TestTxNoncerSetAll(t *testing.T) {
	db := newMockReadState()
	noncer := newTxNoncer(db)

	addr1 := types.Address{0x01}
	addr2 := types.Address{0x02}

	all := map[types.Address]uint64{
		addr1: 10,
		addr2: 20,
	}

	noncer.setAll(all)

	if noncer.get(addr1) != 10 {
		t.Errorf("get(addr1) = %d, want 10", noncer.get(addr1))
	}
	if noncer.get(addr2) != 20 {
		t.Errorf("get(addr2) = %d, want 20", noncer.get(addr2))
	}

	t.Logf("✓ txNoncer.setAll works correctly")
}

func TestTxNoncerConcurrency(t *testing.T) {
	db := newMockReadState()
	noncer := newTxNoncer(db)
	addr := types.Address{0x01}

	done := make(chan bool, 10)

	// Concurrent reads and writes
	for i := 0; i < 10; i++ {
		go func(n int) {
			noncer.set(addr, uint64(n))
			_ = noncer.get(addr)
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	// Should not panic, value is indeterminate but valid
	_ = noncer.get(addr)

	t.Logf("✓ txNoncer is concurrent-safe")
}

// =============================================================================
// ReadState Interface Tests
// =============================================================================

func TestReadStateInterface(t *testing.T) {
	var _ ReadState = newMockReadState()
	t.Logf("✓ ReadState interface is correctly defined")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkTxNoncerGet(b *testing.B) {
	db := newMockReadState()
	addr := types.Address{0x01}
	db.setNonce(addr, 100)
	noncer := newTxNoncer(db)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		noncer.get(addr)
	}
}

func BenchmarkTxNoncerSet(b *testing.B) {
	db := newMockReadState()
	noncer := newTxNoncer(db)
	addr := types.Address{0x01}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		noncer.set(addr, uint64(i))
	}
}

func BenchmarkTxNoncerSetIfLower(b *testing.B) {
	db := newMockReadState()
	addr := types.Address{0x01}
	db.setNonce(addr, 1000000)
	noncer := newTxNoncer(db)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		noncer.setIfLower(addr, uint64(i%1000))
	}
}

func BenchmarkTxNoncerSetAll(b *testing.B) {
	db := newMockReadState()
	noncer := newTxNoncer(db)

	all := make(map[types.Address]uint64)
	for i := 0; i < 100; i++ {
		all[types.Address{byte(i)}] = uint64(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		noncer.setAll(all)
	}
}

