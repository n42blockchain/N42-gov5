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
// PlainStateWriter: plain-state writer with optional changeset capture.
// putDel is the Putter/Deleter intersection used for the target DB.
// NewPlainStateWriter wires a ChangeSetWriter for history buckets;
// NewPlainStateWriterNoHistory disables changeset recording for
// state rebuild paths. UpdateAccountData skips MDBX writes when
// original.Equals(new) to avoid redundant writes on read-only touches.

package state

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

type putDel interface {
	kv.Putter
	kv.Deleter
}
type PlainStateWriter struct {
	db             putDel
	csw            *ChangeSetWriter
	accountIncarns map[types.Address]uint16
}

func NewPlainStateWriter(db putDel, changeSetsDB kv.RwTx, blockNumber uint64) *PlainStateWriter {
	return &PlainStateWriter{
		db:             db,
		csw:            NewChangeSetWriterPlain(changeSetsDB, blockNumber),
		accountIncarns: make(map[types.Address]uint16),
	}
}

func NewPlainStateWriterNoHistory(db putDel) *PlainStateWriter {
	return &PlainStateWriter{
		db:             db,
		accountIncarns: make(map[types.Address]uint16),
	}
}

func (w *PlainStateWriter) UpdateAccountData(address types.Address, original, account *account.StateAccount) error {
	if w.csw != nil {
		if err := w.csw.UpdateAccountData(address, original, account); err != nil {
			return err
		}
	}
	// Skip MDBX write if account data hasn't actually changed.
	// Many accounts are "dirty" (touched by Exist/GetBalance) but unmodified.
	if original != nil && original.Equals(account) {
		return nil
	}
	if err := w.db.Put(modules.Account, address[:], account.MarshalV2()); err != nil {
		return err
	}
	if incarnation, ok := w.accountIncarns[address]; ok {
		if incarnation == 0 {
			if err := w.db.Delete(modules.IncarnationMap, address[:]); err != nil {
				return err
			}
		} else {
			var enc [2]byte
			binary.BigEndian.PutUint16(enc[:], incarnation)
			if err := w.db.Put(modules.IncarnationMap, address[:], enc[:]); err != nil {
				return err
			}
		}
	}
	if account == nil || account.IsEmptyCodeHash() {
		return w.db.Delete(modules.PlainContractCode, modules.PlainGenerateStoragePrefix(address[:]))
	}
	return nil
}

func (w *PlainStateWriter) UpdateAccountCode(address types.Address, incarnation uint16, codeHash types.Hash, code []byte) error {
	if w.csw != nil {
		if err := w.csw.UpdateAccountCode(address, incarnation, codeHash, code); err != nil {
			return err
		}
	}
	if err := w.db.Put(modules.Code, codeHash[:], code); err != nil {
		return err
	}
	if incarnation > 0 {
		var enc [2]byte
		binary.BigEndian.PutUint16(enc[:], incarnation)
		if err := w.db.Put(modules.IncarnationMap, address[:], enc[:]); err != nil {
			return err
		}
		if err := w.db.Put(modules.PlainContractCode, modules.PlainGenerateStoragePrefixWithIncarnation(address[:], incarnation), codeHash[:]); err != nil {
			return err
		}
	}
	return w.db.Put(modules.PlainContractCode, modules.PlainGenerateStoragePrefix(address[:]), codeHash[:])
}

func (w *PlainStateWriter) DeleteAccount(address types.Address, original *account.StateAccount) error {
	if w.csw != nil {
		if err := w.csw.DeleteAccount(address, original); err != nil {
			return err
		}
	}
	if err := w.db.Delete(modules.Account, address[:]); err != nil {
		return err
	}
	if err := w.db.Delete(modules.PlainContractCode, modules.PlainGenerateStoragePrefix(address[:])); err != nil {
		return err
	}
	if incarnation, ok := w.accountIncarns[address]; ok {
		if incarnation == 0 {
			return w.db.Delete(modules.IncarnationMap, address[:])
		}
		var enc [2]byte
		binary.BigEndian.PutUint16(enc[:], incarnation)
		return w.db.Put(modules.IncarnationMap, address[:], enc[:])
	}
	return nil
}

func (w *PlainStateWriter) WriteAccountStorage(address types.Address, incarnation uint16, key *types.Hash, original, value *uint256.Int) error {
	if w.csw != nil {
		if err := w.csw.WriteAccountStorage(address, incarnation, key, original, value); err != nil {
			return err
		}
	}
	if *original == *value {
		return nil
	}
	compositeKey := modules.PlainGenerateCompositeStorageKey(address.Bytes(), key.Bytes())

	v := value.Bytes()
	if len(v) == 0 {
		return w.db.Delete(modules.Storage, compositeKey)
	}
	return w.db.Put(modules.Storage, compositeKey, v)
}

func (w *PlainStateWriter) CreateContract(address types.Address) error {
	// Before the wipe, enumerate pre-destruction slots and record them in
	// the storage changeset with first-wins semantics, so backward unwind
	// past a SELFDESTRUCT can restore the wiped state. Mirrors reth's
	// write_state_reverts wiped-storage enumeration path.
	//
	// After EIP-6780, SELFDESTRUCT only fires in same-tx as CREATE, so
	// storage is nearly empty and the scan is almost free.
	if w.csw != nil {
		slots, err := w.collectPreWipeSlots(address)
		if err != nil {
			return fmt.Errorf("collect pre-wipe slots for %x: %w", address, err)
		}
		w.csw.recordStorageWipe(address, slots)
		if err := w.csw.CreateContract(address); err != nil {
			return err
		}
	}
	return w.wipeAccountStorage(address)
}

// collectPreWipeSlots returns all MDBX storage slots currently persisted for
// the given address. Because PlainStateWriter writes directly to MDBX on
// each SSTORE (no buffer layer), slots touched earlier in the same block
// already appear with their post-SSTORE values; csw's first-wins preserves
// the block-origin values captured by the earlier WriteAccountStorage calls.
func (w *PlainStateWriter) collectPreWipeSlots(address types.Address) (map[types.Hash][]byte, error) {
	out := make(map[types.Hash][]byte)
	cp, ok := w.db.(cursorProvider)
	if !ok {
		return out, nil
	}
	cursor, err := cp.Cursor(modules.Storage)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()
	prefix := address[:]
	for k, v, err := cursor.Seek(prefix); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			return nil, err
		}
		if len(k) < 20 || !bytes.Equal(k[:20], prefix) {
			break
		}
		if len(k) != 52 || len(v) == 0 {
			continue
		}
		var slot types.Hash
		copy(slot[:], k[20:52])
		out[slot] = v
	}
	return out, nil
}

type cursorProvider interface {
	Cursor(table string) (kv.Cursor, error)
}

func (w *PlainStateWriter) wipeAccountStorage(addr types.Address) error {
	prefix := addr[:]
	cp, ok := w.db.(cursorProvider)
	if !ok {
		// No cursor support (e.g. ethdb.Database batch) — nothing to wipe from disk.
		return nil
	}
	cursor, err := cp.Cursor(modules.Storage)
	if err != nil {
		return err
	}
	var keysToDelete [][]byte
	for k, _, err := cursor.Seek(prefix); k != nil; k, _, err = cursor.Next() {
		if err != nil {
			cursor.Close()
			return err
		}
		if len(k) < 20 || !bytes.Equal(k[:20], prefix) {
			break
		}
		keysToDelete = append(keysToDelete, append([]byte{}, k...))
	}
	cursor.Close()
	for _, k := range keysToDelete {
		if err := w.db.Delete(modules.Storage, k); err != nil {
			return err
		}
	}
	return nil
}

func (w *PlainStateWriter) WriteChangeSets() error {
	if w.csw != nil {
		return w.csw.WriteChangeSets()
	}

	return nil
}

func (w *PlainStateWriter) WriteHistory() error {
	if w.csw != nil {
		return w.csw.WriteHistory()
	}

	return nil
}

func (w *PlainStateWriter) ChangeSetWriter() *ChangeSetWriter {
	return w.csw
}

func (w *PlainStateWriter) NoteAccountIncarnations(address types.Address, originalIncarnation, currentIncarnation uint16) {
	if w.csw != nil {
		w.csw.NoteOriginalIncarnation(address, originalIncarnation)
	}
	w.accountIncarns[address] = currentIncarnation
}
