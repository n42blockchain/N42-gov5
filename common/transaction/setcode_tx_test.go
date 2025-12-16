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
// SetCodeTx Type Tests
// =============================================================================

func TestSetCodeTxType(t *testing.T) {
	tx := &SetCodeTx{}
	if tx.txType() != SetCodeTxType {
		t.Errorf("SetCodeTx.txType() = %d, want %d", tx.txType(), SetCodeTxType)
	}
}

func TestSetCodeTxTypeValue(t *testing.T) {
	// EIP-7702 SetCode transaction type should be 0x04
	if SetCodeTxType != 0x04 {
		t.Errorf("SetCodeTxType = 0x%02x, want 0x04", SetCodeTxType)
	}
}

// =============================================================================
// SetCodeTx Field Tests
// =============================================================================

func TestSetCodeTxChainID(t *testing.T) {
	chainID := uint256.NewInt(1)
	tx := &SetCodeTx{ChainID: chainID}

	got := tx.chainID()
	if got.Cmp(chainID) != 0 {
		t.Errorf("SetCodeTx.chainID() = %v, want %v", got, chainID)
	}
}

func TestSetCodeTxNonce(t *testing.T) {
	tx := &SetCodeTx{Nonce: 42}
	if tx.nonce() != 42 {
		t.Errorf("SetCodeTx.nonce() = %d, want 42", tx.nonce())
	}
}

func TestSetCodeTxGas(t *testing.T) {
	tx := &SetCodeTx{Gas: 21000}
	if tx.gas() != 21000 {
		t.Errorf("SetCodeTx.gas() = %d, want 21000", tx.gas())
	}
}

func TestSetCodeTxGasTipCap(t *testing.T) {
	gasTipCap := uint256.NewInt(1000000000) // 1 Gwei
	tx := &SetCodeTx{GasTipCap: gasTipCap}

	got := tx.gasTipCap()
	if got.Cmp(gasTipCap) != 0 {
		t.Errorf("SetCodeTx.gasTipCap() = %v, want %v", got, gasTipCap)
	}
}

func TestSetCodeTxGasFeeCap(t *testing.T) {
	gasFeeCap := uint256.NewInt(2000000000) // 2 Gwei
	tx := &SetCodeTx{GasFeeCap: gasFeeCap}

	got := tx.gasFeeCap()
	if got.Cmp(gasFeeCap) != 0 {
		t.Errorf("SetCodeTx.gasFeeCap() = %v, want %v", got, gasFeeCap)
	}
}

func TestSetCodeTxGasPrice(t *testing.T) {
	gasFeeCap := uint256.NewInt(2000000000)
	tx := &SetCodeTx{GasFeeCap: gasFeeCap}

	// gasPrice() should return GasFeeCap
	got := tx.gasPrice()
	if got.Cmp(gasFeeCap) != 0 {
		t.Errorf("SetCodeTx.gasPrice() = %v, want %v", got, gasFeeCap)
	}
}

func TestSetCodeTxValue(t *testing.T) {
	value := uint256.NewInt(1000000000000000000) // 1 ETH
	tx := &SetCodeTx{Value: value}

	got := tx.value()
	if got.Cmp(value) != 0 {
		t.Errorf("SetCodeTx.value() = %v, want %v", got, value)
	}
}

func TestSetCodeTxTo(t *testing.T) {
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")
	tx := &SetCodeTx{To: &addr}

	got := tx.to()
	if got == nil || *got != addr {
		t.Errorf("SetCodeTx.to() = %v, want %v", got, addr)
	}
}

func TestSetCodeTxToNil(t *testing.T) {
	tx := &SetCodeTx{To: nil}

	if tx.to() != nil {
		t.Errorf("SetCodeTx.to() = %v, want nil", tx.to())
	}
}

func TestSetCodeTxData(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03, 0x04}
	tx := &SetCodeTx{Data: data}

	got := tx.data()
	if len(got) != len(data) {
		t.Errorf("SetCodeTx.data() len = %d, want %d", len(got), len(data))
	}
}

func TestSetCodeTxAccessList(t *testing.T) {
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")
	storageKey := types.Hash{}
	accessList := AccessList{{Address: addr, StorageKeys: []types.Hash{storageKey}}}
	tx := &SetCodeTx{AccessList: accessList}

	got := tx.accessList()
	if len(got) != 1 {
		t.Errorf("SetCodeTx.accessList() len = %d, want 1", len(got))
	}
}

// =============================================================================
// Authorization Tests
// =============================================================================

func TestAuthorizationFields(t *testing.T) {
	addr := types.HexToAddress("0xabcdef0123456789abcdef0123456789abcdef01")
	auth := &Authorization{
		ChainID: 1,
		Address: addr,
		Nonce:   10,
	}

	if auth.ChainID != 1 {
		t.Errorf("Authorization.ChainID = %v, want 1", auth.ChainID)
	}
	if auth.Address != addr {
		t.Errorf("Authorization.Address = %v, want %v", auth.Address, addr)
	}
	if auth.Nonce != 10 {
		t.Errorf("Authorization.Nonce = %d, want 10", auth.Nonce)
	}
}

func TestAuthListCopy(t *testing.T) {
	auth1 := &Authorization{
		ChainID: 1,
		Address: types.HexToAddress("0x1111111111111111111111111111111111111111"),
		Nonce:   1,
		V:       uint256.NewInt(27),
		R:       uint256.NewInt(12345),
		S:       uint256.NewInt(67890),
	}
	auth2 := &Authorization{
		ChainID: 1,
		Address: types.HexToAddress("0x2222222222222222222222222222222222222222"),
		Nonce:   2,
	}

	authList := AuthorizationList{auth1, auth2}
	cpy := authList.Copy()

	if len(cpy) != len(authList) {
		t.Errorf("AuthList.Copy() len = %d, want %d", len(cpy), len(authList))
	}

	// Verify deep copy
	cpy[0].Nonce = 999
	if authList[0].Nonce == 999 {
		t.Error("AuthList.Copy() should create a deep copy")
	}
}

// =============================================================================
// SetCodeTx Copy Tests
// =============================================================================

func TestSetCodeTxCopy(t *testing.T) {
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")
	tx := &SetCodeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     42,
		GasTipCap: uint256.NewInt(1000000000),
		GasFeeCap: uint256.NewInt(2000000000),
		Gas:       21000,
		To:        &addr,
		Value:     uint256.NewInt(1000000000000000000),
		Data:      []byte{0x01, 0x02, 0x03},
		AccessList: AccessList{{Address: addr, StorageKeys: []types.Hash{{}}}},
		AuthList: AuthorizationList{{
			ChainID: 1,
			Address: addr,
			Nonce:   1,
		}},
		V: uint256.NewInt(27),
		R: uint256.NewInt(12345),
		S: uint256.NewInt(67890),
	}

	cpyData := tx.copy()
	cpy, ok := cpyData.(*SetCodeTx)
	if !ok {
		t.Fatal("SetCodeTx.copy() did not return *SetCodeTx")
	}

	// Verify values
	if cpy.Nonce != tx.Nonce {
		t.Errorf("copy.Nonce = %d, want %d", cpy.Nonce, tx.Nonce)
	}
	if cpy.Gas != tx.Gas {
		t.Errorf("copy.Gas = %d, want %d", cpy.Gas, tx.Gas)
	}

	// Verify deep copy
	cpy.Nonce = 999
	if tx.Nonce == 999 {
		t.Error("SetCodeTx.copy() should create a deep copy")
	}
}

// =============================================================================
// SetCodeTx Hash Tests
// =============================================================================

func TestSetCodeTxHash(t *testing.T) {
	tx := &SetCodeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     0,
		GasTipCap: uint256.NewInt(1000000000),
		GasFeeCap: uint256.NewInt(2000000000),
		Gas:       21000,
		Value:     uint256.NewInt(0),
		Data:      []byte{},
	}

	hash := tx.hash()

	// Hash should not be zero
	if hash == (types.Hash{}) {
		t.Error("SetCodeTx.hash() should not return zero hash")
	}

	// Hash should be consistent
	hash2 := tx.hash()
	if hash != hash2 {
		t.Error("SetCodeTx.hash() should be consistent")
	}
}

// =============================================================================
// SetCodeTx Signature Tests
// =============================================================================

func TestSetCodeTxRawSignatureValues(t *testing.T) {
	v := uint256.NewInt(27)
	r := uint256.NewInt(12345)
	s := uint256.NewInt(67890)

	tx := &SetCodeTx{V: v, R: r, S: s}

	gotV, gotR, gotS := tx.rawSignatureValues()

	if gotV.Cmp(v) != 0 {
		t.Errorf("rawSignatureValues() V = %v, want %v", gotV, v)
	}
	if gotR.Cmp(r) != 0 {
		t.Errorf("rawSignatureValues() R = %v, want %v", gotR, r)
	}
	if gotS.Cmp(s) != 0 {
		t.Errorf("rawSignatureValues() S = %v, want %v", gotS, s)
	}
}

func TestSetCodeTxSetSignatureValues(t *testing.T) {
	tx := &SetCodeTx{}

	chainID := uint256.NewInt(1)
	v := uint256.NewInt(27)
	r := uint256.NewInt(12345)
	s := uint256.NewInt(67890)

	tx.setSignatureValues(chainID, v, r, s)

	if tx.ChainID.Cmp(chainID) != 0 {
		t.Errorf("setSignatureValues() ChainID = %v, want %v", tx.ChainID, chainID)
	}
	if tx.V.Cmp(v) != 0 {
		t.Errorf("setSignatureValues() V = %v, want %v", tx.V, v)
	}
	if tx.R.Cmp(r) != 0 {
		t.Errorf("setSignatureValues() R = %v, want %v", tx.R, r)
	}
	if tx.S.Cmp(s) != 0 {
		t.Errorf("setSignatureValues() S = %v, want %v", tx.S, s)
	}
}

// =============================================================================
// copyAccessList Tests
// =============================================================================

func TestCopyAccessList(t *testing.T) {
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")
	storageKey := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")
	al := AccessList{{Address: addr, StorageKeys: []types.Hash{storageKey}}}

	cpy := copyAccessList(al)

	if len(cpy) != len(al) {
		t.Errorf("copyAccessList() len = %d, want %d", len(cpy), len(al))
	}

	// Verify deep copy
	cpy[0].Address = types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if al[0].Address == cpy[0].Address {
		t.Error("copyAccessList() should create a deep copy")
	}
}

func TestCopyAccessListNil(t *testing.T) {
	cpy := copyAccessList(nil)
	if cpy != nil {
		t.Errorf("copyAccessList(nil) = %v, want nil", cpy)
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkSetCodeTxHash(b *testing.B) {
	tx := &SetCodeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     0,
		GasTipCap: uint256.NewInt(1000000000),
		GasFeeCap: uint256.NewInt(2000000000),
		Gas:       21000,
		Value:     uint256.NewInt(0),
		Data:      []byte{},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx.txHash = types.Hash{} // Clear cache
		tx.hash()
	}
}

func BenchmarkSetCodeTxCopy(b *testing.B) {
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")
	tx := &SetCodeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     42,
		GasTipCap: uint256.NewInt(1000000000),
		GasFeeCap: uint256.NewInt(2000000000),
		Gas:       21000,
		To:        &addr,
		Value:     uint256.NewInt(1000000000000000000),
		Data:      []byte{0x01, 0x02, 0x03},
		AccessList: AccessList{{Address: addr, StorageKeys: []types.Hash{{}}}},
		V:         uint256.NewInt(27),
		R:         uint256.NewInt(12345),
		S:         uint256.NewInt(67890),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx.copy()
	}
}

func BenchmarkCopyAccessList(b *testing.B) {
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")
	al := AccessList{
		{Address: addr, StorageKeys: []types.Hash{{}, {}, {}}},
		{Address: addr, StorageKeys: []types.Hash{{}, {}}},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copyAccessList(al)
	}
}

func BenchmarkAuthListCopy(b *testing.B) {
	authList := AuthorizationList{
		{ChainID: 1, Address: types.Address{}, Nonce: 1},
		{ChainID: 1, Address: types.Address{}, Nonce: 2},
		{ChainID: 1, Address: types.Address{}, Nonce: 3},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		authList.Copy()
	}
}

