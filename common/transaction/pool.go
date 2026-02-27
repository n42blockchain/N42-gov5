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

var TxDataPool = &sync.Pool{
	New: func() interface{} {
		return &LegacyTx{
			GasPrice: new(uint256.Int),
			Value:    new(uint256.Int),
		}
	},
}

func GetPooledLegacyTx() *LegacyTx {
	return TxDataPool.Get().(*LegacyTx)
}

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

var DynamicFeeTxPool = &sync.Pool{
	New: func() interface{} {
		return &DynamicFeeTx{
			GasTipCap: new(uint256.Int),
			GasFeeCap: new(uint256.Int),
			Value:     new(uint256.Int),
		}
	},
}

func GetPooledDynamicFeeTx() *DynamicFeeTx {
	return DynamicFeeTxPool.Get().(*DynamicFeeTx)
}

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

var Uint256Pool = &sync.Pool{
	New: func() interface{} {
		return new(uint256.Int)
	},
}

func GetUint256() *uint256.Int {
	return Uint256Pool.Get().(*uint256.Int)
}

func PutUint256(v *uint256.Int) {
	if v != nil {
		v.Clear()
		Uint256Pool.Put(v)
	}
}

var ByteBufferPool = &sync.Pool{
	New: func() interface{} {
		b := make([]byte, 0, 256)
		return &b
	},
}

func GetByteBuffer() *[]byte {
	return ByteBufferPool.Get().(*[]byte)
}

func PutByteBuffer(b *[]byte) {
	if b != nil {
		*b = (*b)[:0]
		ByteBufferPool.Put(b)
	}
}

