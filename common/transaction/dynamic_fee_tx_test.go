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

func TestDynamicFeeTxType(t *testing.T) {
	tx := &DynamicFeeTx{}
	if tx.txType() != DynamicFeeTxType {
		t.Errorf("DynamicFeeTx.txType() = %d, want %d", tx.txType(), DynamicFeeTxType)
	}
}

func TestDynamicFeeTxAccessors(t *testing.T) {
	to := types.Address{0x01}
	from := types.Address{0x02}
	tx := &DynamicFeeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     100,
		GasTipCap: uint256.NewInt(1000),
		GasFeeCap: uint256.NewInt(2000),
		Gas:       21000,
		To:        &to,
		From:      &from,
		Value:     uint256.NewInt(100),
		Data:      []byte{0x01, 0x02},
		AccessList: AccessList{
			{Address: types.Address{0x03}, StorageKeys: []types.Hash{{0x04}}},
		},
		V: uint256.NewInt(0),
		R: uint256.NewInt(12345),
		S: uint256.NewInt(67890),
	}

	if tx.nonce() != 100 {
		t.Errorf("nonce() = %d, want 100", tx.nonce())
	}
	if tx.gas() != 21000 {
		t.Errorf("gas() = %d, want 21000", tx.gas())
	}
	if tx.gasTipCap().Cmp(uint256.NewInt(1000)) != 0 {
		t.Error("gasTipCap mismatch")
	}
	if tx.gasFeeCap().Cmp(uint256.NewInt(2000)) != 0 {
		t.Error("gasFeeCap mismatch")
	}
	// gasPrice should return gasFeeCap for DynamicFeeTx
	if tx.gasPrice().Cmp(uint256.NewInt(2000)) != 0 {
		t.Error("gasPrice should equal gasFeeCap for DynamicFeeTx")
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
	if tx.chainID().Cmp(uint256.NewInt(1)) != 0 {
		t.Error("chainID mismatch")
	}
	if tx.authList() != nil {
		t.Error("DynamicFeeTx should have nil authList")
	}
}

func TestDynamicFeeTxCopy(t *testing.T) {
	to := types.Address{0x01}
	from := types.Address{0x02}
	tx := &DynamicFeeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     100,
		GasTipCap: uint256.NewInt(1000),
		GasFeeCap: uint256.NewInt(2000),
		Gas:       21000,
		To:        &to,
		From:      &from,
		Value:     uint256.NewInt(100),
		Data:      []byte{0x01, 0x02},
		AccessList: AccessList{
			{Address: types.Address{0x03}, StorageKeys: []types.Hash{{0x04}}},
		},
		V: uint256.NewInt(0),
		R: uint256.NewInt(12345),
		S: uint256.NewInt(67890),
	}

	cpy := tx.copy().(*DynamicFeeTx)

	// Verify copy is independent
	if cpy == tx {
		t.Error("copy should return new instance")
	}
	if cpy.Nonce != tx.Nonce {
		t.Error("Nonce not copied correctly")
	}
	if cpy.Gas != tx.Gas {
		t.Error("Gas not copied correctly")
	}
	if cpy.Value.Cmp(tx.Value) != 0 {
		t.Error("Value not copied correctly")
	}
	if cpy.GasTipCap.Cmp(tx.GasTipCap) != 0 {
		t.Error("GasTipCap not copied correctly")
	}
	if cpy.GasFeeCap.Cmp(tx.GasFeeCap) != 0 {
		t.Error("GasFeeCap not copied correctly")
	}
	if len(cpy.AccessList) != len(tx.AccessList) {
		t.Error("AccessList not copied correctly")
	}

	// Modify original and verify copy is unchanged
	tx.Nonce = 200
	if cpy.Nonce != 100 {
		t.Error("copy should be independent of original")
	}

	tx.Value.SetUint64(999)
	if cpy.Value.Cmp(uint256.NewInt(100)) != 0 {
		t.Error("copy Value should be independent")
	}
}

func TestDynamicFeeTxCopyNilValues(t *testing.T) {
	tx := &DynamicFeeTx{
		Nonce: 100,
		Gas:   21000,
		// All pointer fields are nil
	}

	cpy := tx.copy().(*DynamicFeeTx)

	if cpy.Nonce != 100 {
		t.Error("Nonce not copied correctly")
	}
	if cpy.Gas != 21000 {
		t.Error("Gas not copied correctly")
	}
	// Verify nil values are handled
	if cpy.Value == nil {
		t.Error("Value should be initialized even if original is nil")
	}
	if cpy.ChainID == nil {
		t.Error("ChainID should be initialized even if original is nil")
	}
}

func TestDynamicFeeTxHash(t *testing.T) {
	tx := &DynamicFeeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     100,
		GasTipCap: uint256.NewInt(1000),
		GasFeeCap: uint256.NewInt(2000),
		Gas:       21000,
		Value:     uint256.NewInt(100),
		Data:      []byte{0x01, 0x02},
	}

	hash1 := tx.hash()
	hash2 := tx.hash()

	if hash1 != hash2 {
		t.Error("hash should be deterministic")
	}

	// Verify hash is non-zero
	if hash1 == (types.Hash{}) {
		t.Error("hash should not be zero")
	}

	// Change a field and verify hash changes
	tx.Nonce = 101
	hash3 := tx.hash()
	if hash1 == hash3 {
		t.Error("hash should change when fields change")
	}
}

func TestDynamicFeeTxRawSignatureValues(t *testing.T) {
	tx := &DynamicFeeTx{
		V: uint256.NewInt(0),
		R: uint256.NewInt(12345),
		S: uint256.NewInt(67890),
	}

	v, r, s := tx.rawSignatureValues()

	if v.Cmp(uint256.NewInt(0)) != 0 {
		t.Error("V mismatch")
	}
	if r.Cmp(uint256.NewInt(12345)) != 0 {
		t.Error("R mismatch")
	}
	if s.Cmp(uint256.NewInt(67890)) != 0 {
		t.Error("S mismatch")
	}
}

func TestDynamicFeeTxSetSignatureValues(t *testing.T) {
	tx := &DynamicFeeTx{}

	chainID := uint256.NewInt(1)
	v := uint256.NewInt(0)
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
}

func TestDynamicFeeTxSign(t *testing.T) {
	tx := &DynamicFeeTx{
		Sign: []byte{0x01, 0x02, 0x03},
	}

	if len(tx.sign()) != 3 {
		t.Errorf("sign() length = %d, want 3", len(tx.sign()))
	}
}

func TestDynamicFeeTxWithAccessList(t *testing.T) {
	tx := &DynamicFeeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     100,
		GasTipCap: uint256.NewInt(1000),
		GasFeeCap: uint256.NewInt(2000),
		Gas:       21000,
		Value:     uint256.NewInt(100),
		AccessList: AccessList{
			{
				Address:     types.Address{0x01},
				StorageKeys: []types.Hash{{0x02}, {0x03}},
			},
			{
				Address:     types.Address{0x04},
				StorageKeys: []types.Hash{{0x05}},
			},
		},
	}

	al := tx.accessList()
	if len(al) != 2 {
		t.Errorf("accessList length = %d, want 2", len(al))
	}
	if al.StorageKeys() != 3 {
		t.Errorf("StorageKeys() = %d, want 3", al.StorageKeys())
	}
}

func BenchmarkDynamicFeeTxCopy(b *testing.B) {
	to := types.Address{0x01}
	tx := &DynamicFeeTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      100,
		GasTipCap:  uint256.NewInt(1000),
		GasFeeCap:  uint256.NewInt(2000),
		Gas:        21000,
		To:         &to,
		Value:      uint256.NewInt(100),
		Data:       make([]byte, 100),
		AccessList: AccessList{{Address: types.Address{0x03}, StorageKeys: make([]types.Hash, 10)}},
		V:          uint256.NewInt(0),
		R:          uint256.NewInt(12345),
		S:          uint256.NewInt(67890),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx.copy()
	}
}

func BenchmarkDynamicFeeTxHash(b *testing.B) {
	tx := &DynamicFeeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     100,
		GasTipCap: uint256.NewInt(1000),
		GasFeeCap: uint256.NewInt(2000),
		Gas:       21000,
		Value:     uint256.NewInt(100),
		Data:      []byte{0x01, 0x02},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx.hash()
	}
}
