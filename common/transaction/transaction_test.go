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

package transaction

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// AccessList Tests
// =============================================================================

func TestAccessListEmpty(t *testing.T) {
	al := AccessList{}

	if al.StorageKeys() != 0 {
		t.Errorf("Empty AccessList.StorageKeys() = %d, want 0", al.StorageKeys())
	}

	t.Logf("✓ Empty AccessList works correctly")
}

func TestAccessListStorageKeys(t *testing.T) {
	al := AccessList{
		{
			Address:     types.Address{0x01},
			StorageKeys: []types.Hash{{0x01}, {0x02}, {0x03}},
		},
		{
			Address:     types.Address{0x02},
			StorageKeys: []types.Hash{{0x04}, {0x05}},
		},
	}

	if al.StorageKeys() != 5 {
		t.Errorf("AccessList.StorageKeys() = %d, want 5", al.StorageKeys())
	}

	t.Logf("✓ AccessList.StorageKeys works correctly")
}

func TestAccessTupleFields(t *testing.T) {
	tuple := AccessTuple{
		Address:     types.Address{0x01, 0x02},
		StorageKeys: []types.Hash{{0x03}, {0x04}},
	}

	if tuple.Address[0] != 0x01 {
		t.Error("AccessTuple.Address mismatch")
	}
	if len(tuple.StorageKeys) != 2 {
		t.Errorf("AccessTuple.StorageKeys length = %d, want 2", len(tuple.StorageKeys))
	}

	t.Logf("✓ AccessTuple fields work correctly")
}

// =============================================================================
// AccessListTx Tests
// =============================================================================

func TestAccessListTxType(t *testing.T) {
	tx := &AccessListTx{}
	if tx.txType() != AccessListTxType {
		t.Errorf("AccessListTx.txType() = %d, want %d", tx.txType(), AccessListTxType)
	}

	t.Logf("✓ AccessListTx.txType works correctly")
}

func TestAccessListTxChainID(t *testing.T) {
	chainID := uint256.NewInt(1)
	tx := &AccessListTx{ChainID: chainID}

	if tx.chainID().Cmp(chainID) != 0 {
		t.Error("AccessListTx.chainID mismatch")
	}

	t.Logf("✓ AccessListTx.chainID works correctly")
}

func TestAccessListTxAccessors(t *testing.T) {
	to := types.Address{0x01}
	from := types.Address{0x02}
	tx := &AccessListTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      100,
		GasPrice:   uint256.NewInt(1000),
		Gas:        21000,
		To:         &to,
		From:       &from,
		Value:      uint256.NewInt(100),
		Data:       []byte{0x01, 0x02},
		AccessList: AccessList{{Address: types.Address{0x03}, StorageKeys: []types.Hash{{0x04}}}},
	}

	if tx.nonce() != 100 {
		t.Errorf("nonce() = %d, want 100", tx.nonce())
	}
	if tx.gas() != 21000 {
		t.Errorf("gas() = %d, want 21000", tx.gas())
	}
	if tx.gasPrice().Cmp(uint256.NewInt(1000)) != 0 {
		t.Error("gasPrice mismatch")
	}
	if tx.gasTipCap().Cmp(tx.gasPrice()) != 0 {
		t.Error("gasTipCap should equal gasPrice for AccessListTx")
	}
	if tx.gasFeeCap().Cmp(tx.gasPrice()) != 0 {
		t.Error("gasFeeCap should equal gasPrice for AccessListTx")
	}
	if tx.value().Cmp(uint256.NewInt(100)) != 0 {
		t.Error("value mismatch")
	}
	if len(tx.data()) != 2 {
		t.Errorf("data length = %d, want 2", len(tx.data()))
	}
	if tx.to() == nil || *tx.to() != to {
		t.Error("to mismatch")
	}
	if tx.from() == nil || *tx.from() != from {
		t.Error("from mismatch")
	}
	if len(tx.accessList()) != 1 {
		t.Errorf("accessList length = %d, want 1", len(tx.accessList()))
	}

	t.Logf("✓ AccessListTx accessors work correctly")
}

func TestAccessListTxCopy(t *testing.T) {
	to := types.Address{0x01}
	from := types.Address{0x02}
	tx := &AccessListTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      100,
		GasPrice:   uint256.NewInt(1000),
		Gas:        21000,
		To:         &to,
		From:       &from,
		Value:      uint256.NewInt(100),
		Data:       []byte{0x01, 0x02},
		AccessList: AccessList{{Address: types.Address{0x03}, StorageKeys: []types.Hash{{0x04}}}},
	}

	cpy := tx.copy().(*AccessListTx)

	// Verify copy is independent
	if cpy == tx {
		t.Error("copy should return new instance")
	}
	if cpy.nonce() != tx.nonce() {
		t.Error("nonce not copied correctly")
	}
	if cpy.gas() != tx.gas() {
		t.Error("gas not copied correctly")
	}
	if cpy.Value.Cmp(tx.Value) != 0 {
		t.Error("value not copied correctly")
	}

	// Modify original and verify copy is unchanged
	tx.Nonce = 200
	if cpy.nonce() != 100 {
		t.Error("copy should be independent of original")
	}

	t.Logf("✓ AccessListTx.copy works correctly")
}

func TestAccessListTxHash(t *testing.T) {
	tx := &AccessListTx{
		ChainID:  uint256.NewInt(1),
		Nonce:    100,
		GasPrice: uint256.NewInt(1000),
		Gas:      21000,
		Value:    uint256.NewInt(100),
		Data:     []byte{0x01, 0x02},
	}

	hash1 := tx.hash()
	hash2 := tx.hash()

	if hash1 != hash2 {
		t.Error("hash should be deterministic")
	}

	// Change a field and verify hash changes
	tx.Nonce = 101
	hash3 := tx.hash()
	if hash1 == hash3 {
		t.Error("hash should change when fields change")
	}

	t.Logf("✓ AccessListTx.hash works correctly")
}

func TestAccessListTxSignatureValues(t *testing.T) {
	tx := &AccessListTx{
		V: uint256.NewInt(27),
		R: uint256.NewInt(12345),
		S: uint256.NewInt(67890),
	}

	v, r, s := tx.rawSignatureValues()

	if v.Cmp(uint256.NewInt(27)) != 0 {
		t.Error("V mismatch")
	}
	if r.Cmp(uint256.NewInt(12345)) != 0 {
		t.Error("R mismatch")
	}
	if s.Cmp(uint256.NewInt(67890)) != 0 {
		t.Error("S mismatch")
	}

	t.Logf("✓ AccessListTx.rawSignatureValues works correctly")
}

func TestAccessListTxSetSignatureValues(t *testing.T) {
	tx := &AccessListTx{}

	chainID := uint256.NewInt(1)
	v := uint256.NewInt(27)
	r := uint256.NewInt(12345)
	s := uint256.NewInt(67890)

	tx.setSignatureValues(chainID, v, r, s)

	if tx.ChainID.Cmp(chainID) != 0 {
		t.Error("ChainID not set")
	}
	if tx.V.Cmp(v) != 0 {
		t.Error("V not set")
	}
	if tx.R.Cmp(r) != 0 {
		t.Error("R not set")
	}
	if tx.S.Cmp(s) != 0 {
		t.Error("S not set")
	}

	t.Logf("✓ AccessListTx.setSignatureValues works correctly")
}

// =============================================================================
// Transaction Type Constants Tests
// =============================================================================

func TestTransactionTypeConstants(t *testing.T) {
	// Verify transaction type constants are distinct
	types := []byte{LegacyTxType, AccessListTxType, DynamicFeeTxType}
	seen := make(map[byte]bool)

	for _, typ := range types {
		if seen[typ] {
			t.Errorf("Duplicate transaction type: %d", typ)
		}
		seen[typ] = true
	}

	t.Logf("✓ Transaction type constants are unique")
}

// =============================================================================
// Helper Function Tests
// =============================================================================

func TestCopyAddressPtr(t *testing.T) {
	addr := types.Address{0x01, 0x02}
	copied := copyAddressPtr(&addr)

	if copied == nil {
		t.Fatal("copyAddressPtr should not return nil")
	}
	if *copied != addr {
		t.Error("copied address content mismatch")
	}

	// Verify it's a new pointer
	if copied == &addr {
		t.Error("copyAddressPtr should return new pointer")
	}

	// Test nil input
	if copyAddressPtr(nil) != nil {
		t.Error("copyAddressPtr(nil) should return nil")
	}

	t.Logf("✓ copyAddressPtr works correctly")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkAccessListStorageKeys(b *testing.B) {
	al := AccessList{
		{Address: types.Address{0x01}, StorageKeys: make([]types.Hash, 100)},
		{Address: types.Address{0x02}, StorageKeys: make([]types.Hash, 50)},
		{Address: types.Address{0x03}, StorageKeys: make([]types.Hash, 25)},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		al.StorageKeys()
	}
}

func BenchmarkAccessListTxCopy(b *testing.B) {
	to := types.Address{0x01}
	tx := &AccessListTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      100,
		GasPrice:   uint256.NewInt(1000),
		Gas:        21000,
		To:         &to,
		Value:      uint256.NewInt(100),
		Data:       make([]byte, 100),
		AccessList: AccessList{{Address: types.Address{0x03}, StorageKeys: make([]types.Hash, 10)}},
		V:          uint256.NewInt(27),
		R:          uint256.NewInt(12345),
		S:          uint256.NewInt(67890),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx.copy()
	}
}

func BenchmarkAccessListTxHash(b *testing.B) {
	tx := &AccessListTx{
		ChainID:  uint256.NewInt(1),
		Nonce:    100,
		GasPrice: uint256.NewInt(1000),
		Gas:      21000,
		Value:    uint256.NewInt(100),
		Data:     []byte{0x01, 0x02},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx.hash()
	}
}

func BenchmarkAccessListTxAccessors(b *testing.B) {
	to := types.Address{0x01}
	tx := &AccessListTx{
		ChainID:  uint256.NewInt(1),
		Nonce:    100,
		GasPrice: uint256.NewInt(1000),
		Gas:      21000,
		To:       &to,
		Value:    uint256.NewInt(100),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tx.nonce()
		_ = tx.gas()
		_ = tx.gasPrice()
		_ = tx.value()
		_ = tx.to()
	}
}
