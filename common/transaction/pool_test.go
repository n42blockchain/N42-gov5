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
)

// =============================================================================
// LegacyTx Pool Tests
// =============================================================================

func TestGetPooledLegacyTx(t *testing.T) {
	tx := GetPooledLegacyTx()

	if tx == nil {
		t.Fatal("GetPooledLegacyTx returned nil")
	}

	// Pooled tx should have initialized fields
	if tx.GasPrice == nil {
		t.Error("GasPrice should be initialized")
	}
	if tx.Value == nil {
		t.Error("Value should be initialized")
	}
}

func TestPutPooledLegacyTx(t *testing.T) {
	tx := GetPooledLegacyTx()

	// Set some values
	tx.Nonce = 100
	tx.GasPrice.SetUint64(1000)
	tx.Gas = 21000
	tx.Value.SetUint64(100)
	tx.Data = []byte{0x01, 0x02}
	tx.V = uint256.NewInt(27)
	tx.R = uint256.NewInt(12345)
	tx.S = uint256.NewInt(67890)

	// Put back to pool
	PutPooledLegacyTx(tx)

	// Get again and verify it's cleared
	tx2 := GetPooledLegacyTx()
	if tx2.Nonce != 0 {
		t.Error("Nonce should be cleared")
	}
	if tx2.Gas != 0 {
		t.Error("Gas should be cleared")
	}
	if tx2.Data != nil {
		t.Error("Data should be cleared")
	}
	if tx2.V != nil {
		t.Error("V should be cleared")
	}
	if tx2.R != nil {
		t.Error("R should be cleared")
	}
	if tx2.S != nil {
		t.Error("S should be cleared")
	}
}

func TestPutPooledLegacyTxNil(t *testing.T) {
	// Should not panic
	PutPooledLegacyTx(nil)
}

// =============================================================================
// DynamicFeeTx Pool Tests
// =============================================================================

func TestGetPooledDynamicFeeTx(t *testing.T) {
	tx := GetPooledDynamicFeeTx()

	if tx == nil {
		t.Fatal("GetPooledDynamicFeeTx returned nil")
	}

	// Pooled tx should have initialized fields
	if tx.GasTipCap == nil {
		t.Error("GasTipCap should be initialized")
	}
	if tx.GasFeeCap == nil {
		t.Error("GasFeeCap should be initialized")
	}
	if tx.Value == nil {
		t.Error("Value should be initialized")
	}
}

func TestPutPooledDynamicFeeTx(t *testing.T) {
	tx := GetPooledDynamicFeeTx()

	// Set some values
	tx.ChainID = uint256.NewInt(1)
	tx.Nonce = 100
	tx.GasTipCap.SetUint64(1000)
	tx.GasFeeCap.SetUint64(2000)
	tx.Gas = 21000
	tx.Value.SetUint64(100)
	tx.Data = []byte{0x01, 0x02}
	tx.AccessList = AccessList{{}}
	tx.V = uint256.NewInt(0)
	tx.R = uint256.NewInt(12345)
	tx.S = uint256.NewInt(67890)

	// Put back to pool
	PutPooledDynamicFeeTx(tx)

	// Get again and verify it's cleared
	tx2 := GetPooledDynamicFeeTx()
	if tx2.ChainID != nil {
		t.Error("ChainID should be cleared")
	}
	if tx2.Nonce != 0 {
		t.Error("Nonce should be cleared")
	}
	if tx2.Gas != 0 {
		t.Error("Gas should be cleared")
	}
	if tx2.Data != nil {
		t.Error("Data should be cleared")
	}
	if tx2.AccessList != nil {
		t.Error("AccessList should be cleared")
	}
	if tx2.V != nil {
		t.Error("V should be cleared")
	}
	if tx2.R != nil {
		t.Error("R should be cleared")
	}
	if tx2.S != nil {
		t.Error("S should be cleared")
	}
}

func TestPutPooledDynamicFeeTxNil(t *testing.T) {
	// Should not panic
	PutPooledDynamicFeeTx(nil)
}

// =============================================================================
// Uint256 Pool Tests
// =============================================================================

func TestGetUint256(t *testing.T) {
	v := GetUint256()

	if v == nil {
		t.Fatal("GetUint256 returned nil")
	}
}

func TestPutUint256(t *testing.T) {
	v := GetUint256()
	v.SetUint64(12345)

	// Put back to pool
	PutUint256(v)

	// Get again - may or may not be the same instance
	v2 := GetUint256()
	if v2 == nil {
		t.Error("GetUint256 returned nil after Put")
	}
}

func TestPutUint256Nil(t *testing.T) {
	// Should not panic
	PutUint256(nil)
}

// =============================================================================
// ByteBuffer Pool Tests
// =============================================================================

func TestGetByteBuffer(t *testing.T) {
	buf := GetByteBuffer()

	if buf == nil {
		t.Fatal("GetByteBuffer returned nil")
	}
	if *buf == nil {
		t.Error("Buffer slice should be initialized")
	}
}

func TestPutByteBuffer(t *testing.T) {
	buf := GetByteBuffer()

	// Append some data
	*buf = append(*buf, 0x01, 0x02, 0x03)

	// Put back to pool
	PutByteBuffer(buf)

	// Get again and verify it's cleared
	buf2 := GetByteBuffer()
	if buf2 == nil {
		t.Fatal("GetByteBuffer returned nil after Put")
	}
	if len(*buf2) != 0 {
		t.Error("Buffer should be cleared after Put")
	}
}

func TestPutByteBufferNil(t *testing.T) {
	// Should not panic
	PutByteBuffer(nil)
}

func TestByteBufferCapacity(t *testing.T) {
	buf := GetByteBuffer()

	// Initial capacity should be at least 256
	if cap(*buf) < 256 {
		t.Errorf("Initial capacity = %d, want >= 256", cap(*buf))
	}
}

// =============================================================================
// Pool Reuse Tests
// =============================================================================

func TestPoolReuse(t *testing.T) {
	// Test that pools can be used multiple times
	for i := 0; i < 10; i++ {
		tx := GetPooledLegacyTx()
		tx.Nonce = uint64(i)
		PutPooledLegacyTx(tx)
	}

	for i := 0; i < 10; i++ {
		tx := GetPooledDynamicFeeTx()
		tx.Nonce = uint64(i)
		PutPooledDynamicFeeTx(tx)
	}

	for i := 0; i < 10; i++ {
		v := GetUint256()
		v.SetUint64(uint64(i))
		PutUint256(v)
	}

	for i := 0; i < 10; i++ {
		buf := GetByteBuffer()
		*buf = append(*buf, byte(i))
		PutByteBuffer(buf)
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkGetPutPooledLegacyTx(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx := GetPooledLegacyTx()
		tx.Nonce = uint64(i)
		PutPooledLegacyTx(tx)
	}
}

func BenchmarkGetPutPooledDynamicFeeTx(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx := GetPooledDynamicFeeTx()
		tx.Nonce = uint64(i)
		PutPooledDynamicFeeTx(tx)
	}
}

func BenchmarkGetPutUint256(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v := GetUint256()
		v.SetUint64(uint64(i))
		PutUint256(v)
	}
}

func BenchmarkGetPutByteBuffer(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := GetByteBuffer()
		*buf = append(*buf, byte(i))
		PutByteBuffer(buf)
	}
}

func BenchmarkNewLegacyTxVsPool(b *testing.B) {
	b.Run("New", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = &LegacyTx{
				GasPrice: new(uint256.Int),
				Value:    new(uint256.Int),
			}
		}
	})

	b.Run("Pool", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			tx := GetPooledLegacyTx()
			PutPooledLegacyTx(tx)
		}
	})
}
