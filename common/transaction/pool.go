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
	"sync"

	"github.com/holiman/uint256"
)

// TxDataPool provides pooled LegacyTx objects to reduce allocations.
var TxDataPool = &sync.Pool{
	New: func() interface{} {
		return &LegacyTx{
			GasPrice: new(uint256.Int),
			Value:    new(uint256.Int),
		}
	},
}

// GetPooledLegacyTx gets a LegacyTx from the pool.
func GetPooledLegacyTx() *LegacyTx {
	return TxDataPool.Get().(*LegacyTx)
}

// PutPooledLegacyTx returns a LegacyTx to the pool after clearing it.
func PutPooledLegacyTx(tx *LegacyTx) {
	if tx == nil {
		return
	}
	// Clear all fields
	tx.Nonce = 0
	tx.GasPrice.Clear()
	tx.Gas = 0
	tx.To = nil
	tx.Value.Clear()
	tx.Data = nil
	tx.V = nil
	tx.R = nil
	tx.S = nil
	TxDataPool.Put(tx)
}

// DynamicFeeTxPool provides pooled DynamicFeeTx objects.
var DynamicFeeTxPool = &sync.Pool{
	New: func() interface{} {
		return &DynamicFeeTx{
			GasTipCap: new(uint256.Int),
			GasFeeCap: new(uint256.Int),
			Value:     new(uint256.Int),
		}
	},
}

// GetPooledDynamicFeeTx gets a DynamicFeeTx from the pool.
func GetPooledDynamicFeeTx() *DynamicFeeTx {
	return DynamicFeeTxPool.Get().(*DynamicFeeTx)
}

// PutPooledDynamicFeeTx returns a DynamicFeeTx to the pool after clearing it.
func PutPooledDynamicFeeTx(tx *DynamicFeeTx) {
	if tx == nil {
		return
	}
	tx.ChainID = nil
	tx.Nonce = 0
	tx.GasTipCap.Clear()
	tx.GasFeeCap.Clear()
	tx.Gas = 0
	tx.To = nil
	tx.Value.Clear()
	tx.Data = nil
	tx.AccessList = nil
	tx.V = nil
	tx.R = nil
	tx.S = nil
	DynamicFeeTxPool.Put(tx)
}

// Uint256Pool for transaction-related uint256 operations.
var Uint256Pool = &sync.Pool{
	New: func() interface{} {
		return new(uint256.Int)
	},
}

// GetUint256 gets a uint256.Int from the pool.
func GetUint256() *uint256.Int {
	return Uint256Pool.Get().(*uint256.Int)
}

// PutUint256 returns a uint256.Int to the pool.
func PutUint256(v *uint256.Int) {
	if v != nil {
		v.Clear()
		Uint256Pool.Put(v)
	}
}

// ByteBufferPool for temporary byte buffers in serialization.
var ByteBufferPool = &sync.Pool{
	New: func() interface{} {
		b := make([]byte, 0, 256)
		return &b
	},
}

// GetByteBuffer gets a byte buffer from the pool.
func GetByteBuffer() *[]byte {
	return ByteBufferPool.Get().(*[]byte)
}

// PutByteBuffer returns a byte buffer to the pool.
func PutByteBuffer(b *[]byte) {
	if b != nil {
		*b = (*b)[:0]
		ByteBufferPool.Put(b)
	}
}

