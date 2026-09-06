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

	full, fullTx, fullInc, found, delta := r.mvs.ReadAccount(key, r.txIndex)
	if found {
		val := composeAccount(full, delta)
		r.rw.RecordAccountRead(key, fullTx, fullInc, false, val, nil, delta != nil)
		if val == nil {
			// Account was deleted by a preceding tx.
			return nil, nil
		}
		acc, err := DecodeAccount(val)
		if err != nil {
			return nil, err
		}
		return acc, nil
	}

	// No full write in the store — read from base, record the base bytes in
	// the writer's encoding so a later write of the same account by a
	// preceding transaction validates by value, and add the deltas.
	acc, err := r.base.ReadAccountData(address)
	if err != nil {
		r.rw.RecordRead(key, -1, 0, true)
		return nil, err
	}
	var enc []byte
	if acc != nil {
		enc, _ = encodeAccount(acc)
	}
	if delta == nil {
		r.rw.RecordAccountRead(key, -1, 0, true, enc, enc, false)
		return acc, nil
	}
	val := composeAccount(enc, delta)
	r.rw.RecordAccountRead(key, -1, 0, true, val, enc, true)
	return DecodeAccount(val)
}

func (r *ParallelStateReader) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	locKey := LocationKey{Address: address, Field: FieldStorage, Slot: *key}

	val, writerTx, writerInc, found := r.mvs.Read(locKey, r.txIndex)
	if found {
		r.rw.RecordRead(locKey, writerTx, writerInc, false)
	} else {
		r.rw.RecordRead(locKey, -1, 0, true)
	}

	wipeKey := LocationKey{Address: address, Field: FieldStorageWipe}
	if wipeVal, wipeTx, wipeInc, wipeFound := r.mvs.Read(wipeKey, r.txIndex); wipeFound && len(wipeVal) > 0 {
		r.rw.RecordRead(wipeKey, wipeTx, wipeInc, false)
		// Wipe overrides when the slot came from base (no MVS writer → pre-wipe
		// data) or the wipe is strictly newer than the slot's writer. An equal
		// txIdx means the same tx wiped then re-wrote the slot, so the explicit
		// slot write wins (do not shadow).
		if !found || wipeTx > writerTx {
			return nil, nil
		}
	}

	if found {
		return val, nil
	}
	return r.base.ReadAccountStorage(address, key)
}

// ReadAccountCode reads contract code, checking MVS first.
func (r *ParallelStateReader) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	key := LocationKey{Address: address, Field: FieldCode}

	val, writerTx, writerInc, found := r.mvs.Read(key, r.txIndex)
	if found {
		r.rw.RecordReadValue(key, writerTx, writerInc, false, val)
		return val, nil
	}

	// Not in MVS — read from base.
	code, err := r.base.ReadAccountCode(address, codeHash)
	if err != nil {
		r.rw.RecordRead(key, -1, 0, true)
		return nil, err
	}
	r.rw.RecordReadValue(key, -1, 0, true, code)
	return code, nil
}

// ReadAccountCodeSize reads code size. Derives from ReadAccountCode.
func (r *ParallelStateReader) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(address, codeHash)
	if err != nil {
		return 0, err
	}
	return len(code), nil
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
	return acc.MarshalV2(), nil
}
