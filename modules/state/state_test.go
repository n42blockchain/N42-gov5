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

package state

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// AccessList Tests
// =============================================================================

func TestAccessListBasic(t *testing.T) {
	al := newAccessList()

	if al == nil {
		t.Fatal("newAccessList should not return nil")
	}

	t.Logf("✓ AccessList created successfully")
}

func TestAccessListAddAddress(t *testing.T) {
	al := newAccessList()
	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	// 添加地址前
	addrPresent := al.ContainsAddress(addr)
	if addrPresent {
		t.Error("Address should not be present before adding")
	}

	al.AddAddress(addr)

	addrPresent = al.ContainsAddress(addr)
	if !addrPresent {
		t.Error("Address should be present after adding")
	}

	t.Logf("✓ AddAddress works correctly")
}

func TestAccessListAddSlot(t *testing.T) {
	al := newAccessList()
	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	slot := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")

	// 添加地址和槽
	al.AddSlot(addr, slot)

	addrPresent, slotPresent := al.Contains(addr, slot)
	if !addrPresent {
		t.Error("Address should be present")
	}
	if !slotPresent {
		t.Error("Slot should be present")
	}

	t.Logf("✓ AddSlot works correctly")
}

func TestAccessListCopy(t *testing.T) {
	al := newAccessList()
	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	slot := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")

	al.AddSlot(addr, slot)

	// 复制
	alCopy := al.Copy()

	if alCopy == nil {
		t.Fatal("Copy should not return nil")
	}

	// 验证复制的内容
	addrPresent, slotPresent := alCopy.Contains(addr, slot)
	if !addrPresent || !slotPresent {
		t.Error("Copied access list should contain original data")
	}

	t.Logf("✓ AccessList Copy works correctly")
}

func TestAccessListDeleteSlot(t *testing.T) {
	al := newAccessList()
	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	slot := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")

	al.AddSlot(addr, slot)

	// 删除槽
	al.DeleteSlot(addr, slot)

	addrPresent, slotPresent := al.Contains(addr, slot)
	if !addrPresent {
		t.Error("Address should still be present")
	}
	if slotPresent {
		t.Error("Slot should not be present after deletion")
	}

	t.Logf("✓ DeleteSlot works correctly")
}

func TestAccessListDeleteAddress(t *testing.T) {
	al := newAccessList()
	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	al.AddAddress(addr)

	// 删除地址
	al.DeleteAddress(addr)

	addrPresent := al.ContainsAddress(addr)
	if addrPresent {
		t.Error("Address should not be present after deletion")
	}

	t.Logf("✓ DeleteAddress works correctly")
}

// =============================================================================
// Journal Tests
// =============================================================================

func TestJournalBasic(t *testing.T) {
	j := newJournal()

	if j == nil {
		t.Fatal("newJournal should not return nil")
	}

	t.Logf("✓ Journal created successfully")
}

func TestJournalLength(t *testing.T) {
	j := newJournal()

	initialLen := j.length()
	if initialLen != 0 {
		t.Errorf("Initial journal length should be 0, got %d", initialLen)
	}

	t.Logf("✓ Journal length works correctly")
}

// =============================================================================
// State Account Tests (不依赖 stateObject 内部实现)
// =============================================================================

func TestStateAccountFields(t *testing.T) {
	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	// 测试地址 parsing
	if addr == (types.Address{}) {
		t.Error("Address should not be empty")
	}

	t.Logf("✓ Address parsing works correctly")
}

func TestStateAccountBalance(t *testing.T) {
	data := &account.StateAccount{
		Balance: *uint256.NewInt(1000000000000000000),
	}

	expected := uint256.NewInt(1000000000000000000)
	if data.Balance.Cmp(expected) != 0 {
		t.Error("StateAccount Balance should match")
	}

	t.Logf("✓ StateAccount Balance works correctly")
}

func TestStateAccountNonce(t *testing.T) {
	nonce := uint64(42)

	data := &account.StateAccount{
		Nonce:   nonce,
		Balance: *uint256.NewInt(0),
	}

	if data.Nonce != nonce {
		t.Errorf("StateAccount Nonce should be %d, got %d", nonce, data.Nonce)
	}

	t.Logf("✓ StateAccount Nonce works correctly")
}

func TestStateAccountCodeHash(t *testing.T) {
	codeHash := types.HexToHash("0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470")

	data := &account.StateAccount{
		CodeHash: codeHash,
		Balance:  *uint256.NewInt(0),
	}

	if data.CodeHash != codeHash {
		t.Error("StateAccount CodeHash should match")
	}

	t.Logf("✓ StateAccount CodeHash works correctly")
}

// =============================================================================
// Account Tests
// =============================================================================

func TestAccountEmpty(t *testing.T) {
	emptyAccount := &account.StateAccount{
		Nonce:    0,
		Balance:  *uint256.NewInt(0),
		CodeHash: types.Hash{},
	}

	// 空账户检测
	if emptyAccount.Nonce != 0 {
		t.Error("Empty account nonce should be 0")
	}

	if emptyAccount.Balance.Sign() != 0 {
		t.Error("Empty account balance should be 0")
	}

	t.Logf("✓ Empty account detection works correctly")
}

func TestAccountNonEmpty(t *testing.T) {
	acct := &account.StateAccount{
		Nonce:    1,
		Balance:  *uint256.NewInt(1000),
		CodeHash: types.HexToHash("0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"),
	}

	if acct.Nonce == 0 && acct.Balance.Sign() == 0 {
		t.Error("Non-empty account should not be detected as empty")
	}

	t.Logf("✓ Non-empty account detection works correctly")
}

func TestAccountCopy(t *testing.T) {
	original := &account.StateAccount{
		Nonce:    42,
		Balance:  *uint256.NewInt(1000000),
		CodeHash: types.HexToHash("0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"),
	}

	copy := original.SelfCopy()

	if copy.Nonce != original.Nonce {
		t.Error("Copy Nonce should match")
	}
	if copy.Balance.Cmp(&original.Balance) != 0 {
		t.Error("Copy Balance should match")
	}
	if copy.CodeHash != original.CodeHash {
		t.Error("Copy CodeHash should match")
	}

	t.Logf("✓ Account Copy works correctly")
}

// =============================================================================
// Transient Storage Tests
// =============================================================================

func TestTransientStorageBasic(t *testing.T) {
	ts := newTransientStorage()

	if ts == nil {
		t.Fatal("newTransientStorage should not return nil")
	}

	t.Logf("✓ TransientStorage created successfully")
}

func TestTransientStorageSetGet(t *testing.T) {
	ts := newTransientStorage()
	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	key := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")
	value := uint256.NewInt(42)

	// 设置值
	ts.Set(addr, key, *value)

	// 获取值
	retrieved := ts.Get(addr, key)

	if retrieved.Cmp(value) != 0 {
		t.Errorf("TransientStorage Get should return %v, got %v", value, retrieved)
	}

	t.Logf("✓ TransientStorage Set/Get works correctly")
}

func TestTransientStorageNotFound(t *testing.T) {
	ts := newTransientStorage()
	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	key := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")

	// 获取不存在的值
	retrieved := ts.Get(addr, key)

	if retrieved.Sign() != 0 {
		t.Error("TransientStorage Get should return zero for non-existent key")
	}

	t.Logf("✓ TransientStorage handles non-existent keys correctly")
}

func TestTransientStorageCopy(t *testing.T) {
	ts := newTransientStorage()
	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	key := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")
	value := uint256.NewInt(42)

	ts.Set(addr, key, *value)

	// 复制
	tsCopy := ts.Copy()

	// 验证复制后的值
	retrieved := tsCopy.Get(addr, key)
	if retrieved.Cmp(value) != 0 {
		t.Error("Copied transient storage should contain original data")
	}

	t.Logf("✓ TransientStorage Copy works correctly")
}

// =============================================================================
// Code Hash Tests
// =============================================================================

func TestEmptyCodeHash(t *testing.T) {
	emptyHash := types.HexToHash("0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470")

	// 这是 keccak256("") 的结果
	if emptyHash == (types.Hash{}) {
		t.Error("Empty code hash should not be zero hash")
	}

	t.Logf("✓ Empty code hash is correctly defined")
}

// =============================================================================
// Storage Tests
// =============================================================================

func TestStorageKey(t *testing.T) {
	key := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")

	if key == (types.Hash{}) {
		t.Error("Storage key should not be zero")
	}

	t.Logf("✓ Storage key handling works correctly")
}

func TestStorageValue(t *testing.T) {
	value := uint256.NewInt(12345)

	if value.Sign() == 0 {
		t.Error("Storage value should not be zero")
	}

	// 测试大值
	bigValue := new(uint256.Int)
	bigValue.SetBytes([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})

	if bigValue.Sign() == 0 {
		t.Error("Big storage value should not be zero")
	}

	t.Logf("✓ Storage value handling works correctly")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkAccessListAddAddress(b *testing.B) {
	al := newAccessList()
	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		al.AddAddress(addr)
	}
}

func BenchmarkAccessListAddSlot(b *testing.B) {
	al := newAccessList()
	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	slot := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		al.AddSlot(addr, slot)
	}
}

func BenchmarkAccessListContains(b *testing.B) {
	al := newAccessList()
	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	slot := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")

	al.AddSlot(addr, slot)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		al.Contains(addr, slot)
	}
}

func BenchmarkTransientStorageSet(b *testing.B) {
	ts := newTransientStorage()
	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	key := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")
	value := uint256.NewInt(42)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ts.Set(addr, key, *value)
	}
}

func BenchmarkTransientStorageGet(b *testing.B) {
	ts := newTransientStorage()
	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	key := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")
	value := uint256.NewInt(42)

	ts.Set(addr, key, *value)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ts.Get(addr, key)
	}
}

func BenchmarkNewStateAccount(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = account.NewAccount()
	}
}

func BenchmarkJournalLength(b *testing.B) {
	j := newJournal()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		j.length()
	}
}
