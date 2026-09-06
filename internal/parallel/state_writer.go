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
	// deltaEligible, when set, says whether an account's update may be
	// recorded as a balance delta: the transaction never observed the
	// address's balance. Set by the executor from the state's balance-read
	// hook.
	deltaEligible func(types.Address) bool
}

// SetDeltaEligible installs the predicate that lets UpdateAccountData record
// a pure balance increase as a delta.
func (w *ParallelStateWriter) SetDeltaEligible(f func(types.Address) bool) {
	w.deltaEligible = f
}

// NewParallelStateWriter creates a writer that captures writes to a ReadWriteSet.
func NewParallelStateWriter(rw *ReadWriteSet) *ParallelStateWriter {
	return &ParallelStateWriter{rw: rw}
}

// UpdateAccountData records an account update.
func (w *ParallelStateWriter) UpdateAccountData(address types.Address, original, acc *account.StateAccount) error {
	key := LocationKey{Address: address, Field: FieldBalance}
	// A pure balance increase on an account whose balance the transaction
	// never observed is a delta: commutative with every other delta on the
	// account, so recipients credited by many transactions stop being a
	// conflict. Any other change (nonce, code, storage root, a decrease, or
	// an observed balance) is a full write.
	if w.deltaEligible != nil && original != nil && acc != nil &&
		original.Nonce == acc.Nonce && original.CodeHash == acc.CodeHash && original.Root == acc.Root &&
		original.Initialised == acc.Initialised && acc.Balance.Gt(&original.Balance) && w.deltaEligible(address) {
		w.rw.RecordDeltaWrite(key, new(uint256.Int).Sub(&acc.Balance, &original.Balance))
		return nil
	}
	data, err := encodeAccount(acc)
	if err != nil {
		return err
	}
	w.rw.RecordWrite(key, data)
	return nil
}

// UpdateAccountCode records a code update.
func (w *ParallelStateWriter) UpdateAccountCode(address types.Address, codeHash types.Hash, code []byte) error {
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
func (w *ParallelStateWriter) WriteAccountStorage(address types.Address, key types.Hash, original, value uint256.Int) error {
	locKey := LocationKey{Address: address, Field: FieldStorage, Slot: key}
	if value.IsZero() {
		w.rw.RecordWrite(locKey, nil) // delete
	} else {
		bl := value.ByteLen()
		v := make([]byte, bl)
		value.WriteToSlice(v)
		w.rw.RecordWrite(locKey, v)
	}
	return nil
}

// CreateContract records contract creation AND a storage-wipe marker.
//
// IntraBlockState.updateAccountWithWipe calls CreateContract on every
// SELFDESTRUCT, recreate-after-destruct and fresh CREATE (see the three call
// sites in intra_block_state.go), so this single marker covers all wipe cases.
// Recording it lets ParallelStateReader.ReadAccountStorage shadow stale
// pre-wipe slots — without it a tx ordered after a SELFDESTRUCT would read the
// destroyed contract's old storage from base (the bug this fix closes).
func (w *ParallelStateWriter) CreateContract(address types.Address) error {
	key := LocationKey{Address: address, Field: FieldExist}
	w.rw.RecordWrite(key, []byte{1})

	wipeKey := LocationKey{Address: address, Field: FieldStorageWipe}
	w.rw.RecordWrite(wipeKey, []byte{1})
	return nil
}

