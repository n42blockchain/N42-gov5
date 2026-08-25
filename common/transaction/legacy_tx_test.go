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
	"math/rand"
	"testing"

	"github.com/holiman/uint256"
	commonhash "github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
)

func TestLegacyTxType(t *testing.T) {
	tx := &LegacyTx{}
	if tx.txType() != LegacyTxType {
		t.Errorf("LegacyTx.txType() = %d, want %d", tx.txType(), LegacyTxType)
	}
}

func TestLegacyTxAccessors(t *testing.T) {
	to := types.Address{0x01}
	from := types.Address{0x02}
	tx := &LegacyTx{
		Nonce:    100,
		GasPrice: uint256.NewInt(1000),
		Gas:      21000,
		To:       &to,
		From:     &from,
		Value:    uint256.NewInt(100),
		Data:     []byte{0x01, 0x02},
		V:        uint256.NewInt(27),
		R:        uint256.NewInt(12345),
		S:        uint256.NewInt(67890),
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
		t.Error("gasTipCap should equal gasPrice for LegacyTx")
	}
	if tx.gasFeeCap().Cmp(tx.gasPrice()) != 0 {
		t.Error("gasFeeCap should equal gasPrice for LegacyTx")
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
	if tx.accessList() != nil {
		t.Error("LegacyTx should have nil accessList")
	}
	if tx.authList() != nil {
		t.Error("LegacyTx should have nil authList")
	}
}

func TestLegacyTxChainID(t *testing.T) {
	tests := []struct {
		name     string
		v        *uint256.Int
		expected *uint256.Int
	}{
		{
			name:     "pre-EIP155 v=27",
			v:        uint256.NewInt(27),
			expected: nil,
		},
		{
			name:     "pre-EIP155 v=28",
			v:        uint256.NewInt(28),
			expected: nil,
		},
		{
			name:     "EIP155 mainnet v=37",
			v:        uint256.NewInt(37),
			expected: uint256.NewInt(1),
		},
		{
			name:     "EIP155 mainnet v=38",
			v:        uint256.NewInt(38),
			expected: uint256.NewInt(1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := &LegacyTx{V: tt.v}
			chainID := tx.chainID()
			if tt.expected == nil {
				if chainID != nil && !chainID.IsZero() {
					t.Errorf("chainID = %v, want nil or 0", chainID)
				}
			} else {
				if chainID == nil || chainID.Cmp(tt.expected) != 0 {
					t.Errorf("chainID = %v, want %v", chainID, tt.expected)
				}
			}
		})
	}
}

func TestLegacyTxCopy(t *testing.T) {
	to := types.Address{0x01}
	from := types.Address{0x02}
	tx := &LegacyTx{
		Nonce:    100,
		GasPrice: uint256.NewInt(1000),
		Gas:      21000,
		To:       &to,
		From:     &from,
		Value:    uint256.NewInt(100),
		Data:     []byte{0x01, 0x02},
		V:        uint256.NewInt(27),
		R:        uint256.NewInt(12345),
		S:        uint256.NewInt(67890),
	}

	cpy := tx.copy().(*LegacyTx)

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
	if cpy.GasPrice.Cmp(tx.GasPrice) != 0 {
		t.Error("GasPrice not copied correctly")
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

func TestLegacyTxCopyNilValues(t *testing.T) {
	tx := &LegacyTx{
		Nonce: 100,
		Gas:   21000,
		// All pointer fields are nil
	}

	cpy := tx.copy().(*LegacyTx)

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
}

func TestLegacyTxHash(t *testing.T) {
	tx := &LegacyTx{
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
}

func TestLegacyTxHashMatchesReflectionEncoder(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 1000; i++ {
		var to *types.Address
		if i%4 != 0 {
			a := types.Address{}
			_, _ = rng.Read(a[:])
			to = &a
		}
		data := make([]byte, i%257)
		_, _ = rng.Read(data)
		tx := &LegacyTx{
			Nonce:    rng.Uint64(),
			GasPrice: randomTestU256(rng, i),
			Gas:      rng.Uint64(),
			To:       to,
			Value:    randomTestU256(rng, i+1),
			Data:     data,
			V:        randomTestU256(rng, i+2),
			R:        randomTestU256(rng, i+3),
			S:        randomTestU256(rng, i+4),
		}
		want := commonhash.RlpHash([]interface{}{
			tx.Nonce, tx.GasPrice, tx.Gas, tx.To, tx.Value, tx.Data,
			tx.V, tx.R, tx.S,
		})
		if got := tx.hash(); got != want {
			t.Fatalf("case %d: hash mismatch\n got  %x\n want %x", i, got, want)
		}
	}
}

func randomTestU256(rng *rand.Rand, variant int) *uint256.Int {
	switch variant % 5 {
	case 0:
		return nil
	case 1:
		return new(uint256.Int)
	case 2:
		return uint256.NewInt(uint64(variant % 128))
	default:
		var raw [32]byte
		_, _ = rng.Read(raw[:])
		return new(uint256.Int).SetBytes(raw[:])
	}
}

func TestLegacyTxRawSignatureValues(t *testing.T) {
	tx := &LegacyTx{
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
}

func TestLegacyTxSetSignatureValues(t *testing.T) {
	tx := &LegacyTx{}

	v := uint256.NewInt(27)
	r := uint256.NewInt(12345)
	s := uint256.NewInt(67890)

	tx.setSignatureValues(nil, v, r, s)

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

func TestNewTransaction(t *testing.T) {
	from := types.Address{0x01}
	to := types.Address{0x02}
	amount := uint256.NewInt(100)
	gasLimit := uint64(21000)
	gasPrice := uint256.NewInt(1000)
	data := []byte{0x01, 0x02}

	tx := NewTransaction(1, from, &to, amount, gasLimit, gasPrice, data)

	if tx == nil {
		t.Fatal("NewTransaction returned nil")
	}
	if tx.Nonce() != 1 {
		t.Errorf("Nonce = %d, want 1", tx.Nonce())
	}
	if tx.Gas() != gasLimit {
		t.Errorf("Gas = %d, want %d", tx.Gas(), gasLimit)
	}
	if tx.Type() != LegacyTxType {
		t.Errorf("Type = %d, want %d", tx.Type(), LegacyTxType)
	}
}

func TestNewContractCreation(t *testing.T) {
	amount := uint256.NewInt(100)
	gasLimit := uint64(100000)
	gasPrice := uint256.NewInt(1000)
	data := []byte{0x60, 0x80, 0x60, 0x40} // Some bytecode

	tx := NewContractCreation(1, amount, gasLimit, gasPrice, data)

	if tx == nil {
		t.Fatal("NewContractCreation returned nil")
	}
	if tx.To() != nil {
		t.Error("Contract creation tx should have nil To")
	}
	if tx.Nonce() != 1 {
		t.Errorf("Nonce = %d, want 1", tx.Nonce())
	}
	if tx.Type() != LegacyTxType {
		t.Errorf("Type = %d, want %d", tx.Type(), LegacyTxType)
	}
}

func TestLegacyTxSign(t *testing.T) {
	tx := &LegacyTx{
		Sign: []byte{0x01, 0x02, 0x03},
	}

	if len(tx.sign()) != 3 {
		t.Errorf("sign() length = %d, want 3", len(tx.sign()))
	}
}

func BenchmarkLegacyTxCopy(b *testing.B) {
	to := types.Address{0x01}
	tx := &LegacyTx{
		Nonce:    100,
		GasPrice: uint256.NewInt(1000),
		Gas:      21000,
		To:       &to,
		Value:    uint256.NewInt(100),
		Data:     make([]byte, 100),
		V:        uint256.NewInt(27),
		R:        uint256.NewInt(12345),
		S:        uint256.NewInt(67890),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx.copy()
	}
}

func BenchmarkLegacyTxHash(b *testing.B) {
	tx := &LegacyTx{
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
