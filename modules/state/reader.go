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
// HistoryStateReader: state-as-of-block reader using history cursors.
// NewStateHistoryReader opens AccountsHistory, StorageHistory,
// AccountChangeSet and StorageChangeSet cursors on tx and binds a
// blockNr pivot. The reader resolves account and storage values at
// that historical checkpoint by combining the history bitmap lookup
// with the corresponding dupsort changeset entry.

package state

import (
	"bytes"
	"fmt"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

type HistoryStateReader struct {
	accHistoryC, storageHistoryC kv.Cursor
	accChangesC, storageChangesC kv.CursorDupSort
	blockNr                      uint64
	tx                           kv.Tx
	db                           kv.Getter
}

func NewStateHistoryReader(tx kv.Tx, db kv.Getter, blockNr uint64) (*HistoryStateReader, error) {
	c1, err := tx.Cursor(modules.AccountsHistory)
	if err != nil {
		return nil, fmt.Errorf("failed to create AccountsHistory cursor: %w", err)
	}
	c2, err := tx.Cursor(modules.StorageHistory)
	if err != nil {
		return nil, fmt.Errorf("failed to create StorageHistory cursor: %w", err)
	}
	c3, err := tx.CursorDupSort(modules.AccountChangeSet)
	if err != nil {
		return nil, fmt.Errorf("failed to create AccountChangeSet cursor: %w", err)
	}
	c4, err := tx.CursorDupSort(modules.StorageChangeSet)
	if err != nil {
		return nil, fmt.Errorf("failed to create StorageChangeSet cursor: %w", err)
	}
	return &HistoryStateReader{
		tx:          tx,
		blockNr:     blockNr,
		db:          db,
		accHistoryC: c1, storageHistoryC: c2, accChangesC: c3, storageChangesC: c4,
	}, nil
}

func (dbr *HistoryStateReader) Rollback() {
	dbr.tx.Rollback()
}

func (dbr *HistoryStateReader) SetBlockNumber(blockNr uint64) {
	dbr.blockNr = blockNr
}

func (dbr *HistoryStateReader) GetOne(bucket string, key []byte) ([]byte, error) {
	if len(bucket) == 0 {
		return nil, nil
	}
	return dbr.db.GetOne(bucket, key)
}

func (r *HistoryStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	// Try current state first.
	v, err := r.db.GetOne(modules.Account, address[:])
	if err != nil {
		return nil, err
	}
	if len(v) > 0 {
		var acc account.StateAccount
		if err := acc.DecodeForStorage(v); err != nil {
			return nil, err
		}
		return &acc, nil
	}

	// Fall back to historical state.
	enc, err := GetAsOf(r.tx, r.accHistoryC, r.accChangesC, false /* storage */, address[:], r.blockNr)
	if err != nil || len(enc) == 0 {
		return nil, nil
	}
	var acc account.StateAccount
	if err := acc.DecodeForStorage(enc); err != nil {
		return nil, err
	}
	return &acc, nil
}

func (r *HistoryStateReader) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	compositeKey := modules.PlainGenerateCompositeStorageKey(address.Bytes(), key.Bytes())
	v, err := r.db.GetOne(modules.Storage, compositeKey)
	if err != nil {
		return nil, err
	}
	if len(v) > 0 {
		return v, nil
	}
	return GetAsOf(r.tx, r.storageHistoryC, r.storageChangesC, true /* storage */, compositeKey, r.blockNr)
}

func (r *HistoryStateReader) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	if bytes.Equal(codeHash[:], emptyCodeHash) {
		return nil, nil
	}
	v, err := r.tx.GetOne(modules.Code, codeHash[:])
	if err != nil {
		return nil, err
	}
	if len(v) == 0 {
		return nil, nil
	}
	return types.CopyBytes(v), nil
}

func (r *HistoryStateReader) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(address, codeHash)
	if err != nil {
		return 0, err
	}
	return len(code), nil
}
