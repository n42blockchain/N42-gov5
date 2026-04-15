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
//
// ChangeSetWriter: StateWriter that accumulates per-block changesets.
// Holds accountChanges, storageChanges and a storageChanged flag map
// keyed by address. NewChangeSetWriterPlain binds an MDBX tx and
// block number for direct persistence; the plain in-memory constructor
// is used by tests. GetAccountChanges materializes a *changeset.ChangeSet
// via changeset.NewAccountChangeSet for encoding and flush.

package state

import (
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/changeset"
)

// ChangeSetWriter is a mock StateWriter that accumulates changes in-memory into ChangeSets.
//
// db is held as kv.Tx (read-only) so per-block construction can pass an
// RoTx safely; methods that mutate MDBX (WriteChangeSets, WriteHistory)
// type-assert to kv.RwTx and return an error if the assertion fails.
// Per-block executor flow only uses db for cursor reads (in
// collectPreWipeSlots); the RwTx-needing paths are exercised exclusively
// by the genesis initializer which constructs the writer with a real
// kv.RwTx.
type ChangeSetWriter struct {
	db             kv.Tx
	accountChanges map[types.Address][]byte
	accountIncarns map[types.Address]uint16
	storageChanged map[types.Address]bool
	storageChanges map[string][]byte
	blockNumber    uint64
}

func NewChangeSetWriter() *ChangeSetWriter {
	return &ChangeSetWriter{
		accountChanges: make(map[types.Address][]byte),
		accountIncarns: make(map[types.Address]uint16),
		storageChanged: make(map[types.Address]bool),
		storageChanges: make(map[string][]byte),
	}
}
func NewChangeSetWriterPlain(db kv.Tx, blockNumber uint64) *ChangeSetWriter {
	return &ChangeSetWriter{
		db:             db,
		accountChanges: make(map[types.Address][]byte),
		accountIncarns: make(map[types.Address]uint16),
		storageChanged: make(map[types.Address]bool),
		storageChanges: make(map[string][]byte),
		blockNumber:    blockNumber,
	}
}

func (w *ChangeSetWriter) GetAccountChanges() (*changeset.ChangeSet, error) {
	cs := changeset.NewAccountChangeSet()
	for address, val := range w.accountChanges {
		if err := cs.Add(types.CopyBytes(address[:]), val); err != nil {
			return nil, err
		}
	}
	return cs, nil
}
func (w *ChangeSetWriter) GetStorageChanges() (*changeset.ChangeSet, error) {
	cs := changeset.NewStorageChangeSet()
	for key, val := range w.storageChanges {
		if err := cs.Add([]byte(key), val); err != nil {
			return nil, err
		}
	}
	return cs, nil
}

func accountsEqual(a1, a2 *account.StateAccount) bool {
	if a1.Nonce != a2.Nonce {
		return false
	}
	if a1.Initialised != a2.Initialised {
		return false
	}
	if a1.Initialised && a1.Balance.Cmp(&a2.Balance) != 0 {
		return false
	}
	if a1.IsEmptyCodeHash() != a2.IsEmptyCodeHash() {
		return false
	}
	if !a1.IsEmptyCodeHash() && a1.CodeHash != a2.CodeHash {
		return false
	}
	return true
}

func (w *ChangeSetWriter) UpdateAccountData(address types.Address, original, account *account.StateAccount) error {
	if !accountsEqual(original, account) || w.storageChanged[address] {
		w.accountChanges[address] = originalAccountData(original, true /*omitHashes*/, w.accountIncarns[address])
	}
	return nil
}

func (w *ChangeSetWriter) UpdateAccountCode(address types.Address, incarnation uint16, codeHash types.Hash, code []byte) error {
	return nil
}

func (w *ChangeSetWriter) DeleteAccount(address types.Address, original *account.StateAccount) error {
	if original == nil || !original.Initialised {
		return nil
	}
	w.accountChanges[address] = originalAccountData(original, true, w.accountIncarns[address])
	return nil
}

func (w *ChangeSetWriter) WriteAccountStorage(address types.Address, incarnation uint16, key *types.Hash, original, value *uint256.Int) error {
	if *original == *value {
		return nil
	}

	compositeKey := modules.PlainGenerateCompositeStorageKey(address.Bytes(), key.Bytes())

	w.storageChanges[string(compositeKey)] = original.Bytes()
	w.storageChanged[address] = true

	return nil
}

func (w *ChangeSetWriter) CreateContract(address types.Address) error {
	return nil
}

func (w *ChangeSetWriter) NoteOriginalIncarnation(address types.Address, incarnation uint16) {
	if incarnation == 0 {
		delete(w.accountIncarns, address)
		return
	}
	w.accountIncarns[address] = incarnation
}

// recordStorageWipe adds (slot, old_value) entries to storageChanges for a
// contract being destroyed via SELFDESTRUCT, with first-wins semantics:
// entries already present (from an earlier SSTORE in the same block) are
// preserved, so the block-origin value wins over any post-SSTORE value.
//
// Without this, storageChanges would omit all slots that existed before the
// destruction but were not individually SSTORE'd in the same block, making
// backward unwind past a SELFDESTRUCT impossible. Mirrors reth's
// write_state_reverts wiped-storage enumeration.
func (w *ChangeSetWriter) recordStorageWipe(address types.Address, slots map[types.Hash][]byte) {
	if len(slots) == 0 {
		return
	}
	for slot, v := range slots {
		compositeKey := modules.PlainGenerateCompositeStorageKey(address[:], slot[:])
		if _, exists := w.storageChanges[string(compositeKey)]; exists {
			continue
		}
		// Copy to decouple from caller's buffer.
		cp := make([]byte, len(v))
		copy(cp, v)
		w.storageChanges[string(compositeKey)] = cp
	}
	w.storageChanged[address] = true
}

func (w *ChangeSetWriter) WriteChangeSets() error {
	rwTx, ok := w.db.(kv.RwTx)
	if !ok {
		return fmt.Errorf("WriteChangeSets requires a kv.RwTx; got %T", w.db)
	}
	accountChanges, err := w.GetAccountChanges()
	if err != nil {
		return err
	}
	if err = changeset.Mapper[modules.AccountChangeSet].Encode(w.blockNumber, accountChanges, func(k, v []byte) error {
		return rwTx.AppendDup(modules.AccountChangeSet, k, v)
	}); err != nil {
		return err
	}

	storageChanges, err := w.GetStorageChanges()
	if err != nil {
		return err
	}
	if storageChanges.Len() == 0 {
		return nil
	}
	return changeset.Mapper[modules.StorageChangeSet].Encode(w.blockNumber, storageChanges, func(k, v []byte) error {
		return rwTx.AppendDup(modules.StorageChangeSet, k, v)
	})
}

func (w *ChangeSetWriter) WriteHistory() error {
	rwTx, ok := w.db.(kv.RwTx)
	if !ok {
		return fmt.Errorf("WriteHistory requires a kv.RwTx; got %T", w.db)
	}
	accountChanges, err := w.GetAccountChanges()
	if err != nil {
		return err
	}
	if err = writeIndex(w.blockNumber, accountChanges, modules.AccountsHistory, rwTx); err != nil {
		return err
	}

	storageChanges, err := w.GetStorageChanges()
	if err != nil {
		return err
	}
	return writeIndex(w.blockNumber, storageChanges, modules.StorageHistory, rwTx)
}

func (w *ChangeSetWriter) PrintChangedAccounts() {
	fmt.Println("Account Changes")
	for k := range w.accountChanges {
		fmt.Println(hexutil.Encode(k.Bytes()))
	}
	fmt.Println("------------------------------------------")
}
