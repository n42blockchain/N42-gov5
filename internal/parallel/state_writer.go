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
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// ParallelStateWriter captures state writes into a ReadWriteSet.
// It does NOT write to the underlying database — the writes are
// recorded for later application to the MVS and eventual commit
// after all transactions are validated.
//
// Implements state.StateWriter interface.
type ParallelStateWriter struct {
	rw *ReadWriteSet
}

// NewParallelStateWriter creates a writer that captures writes to a ReadWriteSet.
func NewParallelStateWriter(rw *ReadWriteSet) *ParallelStateWriter {
	return &ParallelStateWriter{rw: rw}
}

// UpdateAccountData records an account update.
func (w *ParallelStateWriter) UpdateAccountData(address types.Address, original, acc *account.StateAccount) error {
	key := LocationKey{Address: address, Field: FieldBalance}
	data, err := encodeAccount(acc)
	if err != nil {
		return err
	}
	w.rw.RecordWrite(key, data)
	return nil
}

// UpdateAccountCode records a code update.
func (w *ParallelStateWriter) UpdateAccountCode(address types.Address, incarnation uint16, codeHash types.Hash, code []byte) error {
	key := LocationKey{Address: address, Field: FieldCode}
	w.rw.RecordWrite(key, code)

	codeHashKey := LocationKey{Address: address, Field: FieldCodeHash}
	w.rw.RecordWrite(codeHashKey, codeHash[:])

	codeSizeKey := LocationKey{Address: address, Field: FieldCodeSize}
	size := len(code)
	w.rw.RecordWrite(codeSizeKey, []byte{byte(size >> 24), byte(size >> 16), byte(size >> 8), byte(size)})
	return nil
}

// DeleteAccount records an account deletion.
func (w *ParallelStateWriter) DeleteAccount(address types.Address, original *account.StateAccount) error {
	key := LocationKey{Address: address, Field: FieldBalance}
	w.rw.RecordWrite(key, nil) // nil = deleted

	suicideKey := LocationKey{Address: address, Field: FieldSuicide}
	w.rw.RecordWrite(suicideKey, []byte{1})

	// Clear FieldExist so a preceding CreateContract is overridden.
	existKey := LocationKey{Address: address, Field: FieldExist}
	w.rw.RecordWrite(existKey, nil)
	return nil
}

// WriteAccountStorage records a storage slot write.
func (w *ParallelStateWriter) WriteAccountStorage(address types.Address, incarnation uint16, key *types.Hash, original, value *uint256.Int) error {
	locKey := LocationKey{Address: address, Field: FieldStorage, Slot: *key}
	if value.IsZero() {
		w.rw.RecordWrite(locKey, nil) // delete
	} else {
		w.rw.RecordWrite(locKey, value.Bytes())
	}
	return nil
}

// CreateContract records contract creation.
func (w *ParallelStateWriter) CreateContract(address types.Address) error {
	key := LocationKey{Address: address, Field: FieldExist}
	w.rw.RecordWrite(key, []byte{1})
	return nil
}

func (w *ParallelStateWriter) NoteAccountIncarnations(address types.Address, originalIncarnation, currentIncarnation uint16) {
}
