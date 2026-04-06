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

package state

import (
	"bytes"

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
	db  putDel
	csw *ChangeSetWriter
}

func NewPlainStateWriter(db putDel, changeSetsDB kv.RwTx, blockNumber uint64) *PlainStateWriter {
	return &PlainStateWriter{
		db:  db,
		csw: NewChangeSetWriterPlain(changeSetsDB, blockNumber),
	}
}

func NewPlainStateWriterNoHistory(db putDel) *PlainStateWriter {
	return &PlainStateWriter{
		db: db,
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
	return w.db.Put(modules.Account, address[:], account.MarshalV2())
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
	// IncarnationMap removed — incarnation no longer used
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
	if w.csw != nil {
		if err := w.csw.CreateContract(address); err != nil {
			return err
		}
	}
	// Wipe all existing storage for this address (replaces incarnation).
	// After EIP-6780, SELFDESTRUCT only fires in same-tx as CREATE,
	// so storage is nearly empty — this wipe is almost free.
	return w.wipeAccountStorage(address)
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
		if len(k) < 20 || !bytes.HasPrefix(k[:20], prefix) {
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
