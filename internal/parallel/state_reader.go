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

package parallel

import (

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// ParallelStateReader wraps a base StateReader and layers MVS reads on top.
// For each state read, it first checks the MVS for writes from preceding
// transactions. If found, it uses the MVS value; otherwise, it falls back
// to the base reader. All reads are recorded in the ReadWriteSet.
type ParallelStateReader struct {
	base    state.StateReader
	mvs     *MVS
	rw      *ReadWriteSet
	txIndex int
}

// NewParallelStateReader creates a state reader that reads from MVS first,
// then falls back to the base reader.
func NewParallelStateReader(base state.StateReader, mvs *MVS, rw *ReadWriteSet, txIndex int) *ParallelStateReader {
	return &ParallelStateReader{
		base:    base,
		mvs:     mvs,
		rw:      rw,
		txIndex: txIndex,
	}
}

// ReadAccountData reads account data, checking MVS first.
func (r *ParallelStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	key := LocationKey{Address: address, Field: FieldBalance}

	val, writerTx, writerInc, found := r.mvs.Read(key, r.txIndex)
	if found {
		r.rw.RecordRead(key, writerTx, writerInc, false)
		if val == nil {
			// Account was deleted by a preceding tx.
			return nil, nil
		}
		// Decode protobuf-encoded account.
		acc, err := DecodeAccount(val)
		if err != nil {
			return nil, err
		}
		return acc, nil
	}

	// Not in MVS — read from base.
	r.rw.RecordRead(key, -1, 0, true)
	return r.base.ReadAccountData(address)
}

// ReadAccountStorage reads a storage slot, checking MVS first.
func (r *ParallelStateReader) ReadAccountStorage(address types.Address, incarnation uint16, key *types.Hash) ([]byte, error) {
	locKey := LocationKey{Address: address, Field: FieldStorage, Slot: *key}

	val, writerTx, writerInc, found := r.mvs.Read(locKey, r.txIndex)
	if found {
		r.rw.RecordRead(locKey, writerTx, writerInc, false)
		return val, nil
	}

	// Not in MVS — read from base.
	r.rw.RecordRead(locKey, -1, 0, true)
	return r.base.ReadAccountStorage(address, incarnation, key)
}

// ReadAccountCode reads contract code, checking MVS first.
func (r *ParallelStateReader) ReadAccountCode(address types.Address, incarnation uint16, codeHash types.Hash) ([]byte, error) {
	key := LocationKey{Address: address, Field: FieldCode}

	val, writerTx, writerInc, found := r.mvs.Read(key, r.txIndex)
	if found {
		r.rw.RecordRead(key, writerTx, writerInc, false)
		return val, nil
	}

	// Not in MVS — read from base.
	r.rw.RecordRead(key, -1, 0, true)
	return r.base.ReadAccountCode(address, incarnation, codeHash)
}

// ReadAccountCodeSize reads code size. Derives from ReadAccountCode.
func (r *ParallelStateReader) ReadAccountCodeSize(address types.Address, incarnation uint16, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(address, incarnation, codeHash)
	if err != nil {
		return 0, err
	}
	return len(code), nil
}

// ReadAccountIncarnation reads incarnation, checking MVS first.
func (r *ParallelStateReader) ReadAccountIncarnation(address types.Address) (uint16, error) {
	key := LocationKey{Address: address, Field: FieldIncarnation}

	val, writerTx, writerInc, found := r.mvs.Read(key, r.txIndex)
	if found {
		r.rw.RecordRead(key, writerTx, writerInc, false)
		if val == nil || len(val) < 2 {
			return 0, nil
		}
		return uint16(val[0])<<8 | uint16(val[1]), nil
	}

	// Not in MVS — read from base.
	r.rw.RecordRead(key, -1, 0, true)
	return r.base.ReadAccountIncarnation(address)
}

// DecodeAccount decodes a V2-encoded StateAccount.
func DecodeAccount(data []byte) (*account.StateAccount, error) {
	acc := new(account.StateAccount)
	if err := acc.DecodeForStorageV2(data); err != nil {
		return nil, err
	}
	return acc, nil
}

// encodeAccount encodes a StateAccount using V2 variable-length format.
func encodeAccount(acc *account.StateAccount) ([]byte, error) {
	buf := make([]byte, acc.EncodingLengthForStorageV2())
	acc.EncodeForStorageV2(buf)
	return buf, nil
}
