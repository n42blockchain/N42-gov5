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

package snapsync

import (
	"context"
	"encoding/binary"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// Progress key constants for the SnapSyncProgress table.
var (
	keyPivotBlock    = []byte("pivot_block")
	keyAccountCursor = []byte("account_cursor")
	keyState         = []byte("state")
)

// SyncState describes the current snap sync state.
const (
	StateRunning   = "running"
	StateCompleted = "completed"
)

// SavePivotBlock persists the pivot block number.
func SavePivotBlock(tx kv.RwTx, blockNum uint64) error {
	val := make([]byte, 8)
	binary.BigEndian.PutUint64(val, blockNum)
	return tx.Put(modules.SnapSyncProgress, keyPivotBlock, val)
}

// LoadPivotBlock returns the saved pivot block number, or 0 if not set.
func LoadPivotBlock(tx kv.Tx) (uint64, error) {
	val, err := tx.GetOne(modules.SnapSyncProgress, keyPivotBlock)
	if err != nil || val == nil {
		return 0, err
	}
	if len(val) < 8 {
		return 0, nil
	}
	return binary.BigEndian.Uint64(val), nil
}

// SaveAccountCursor persists the last downloaded account address.
// This allows resuming downloads after a crash.
func SaveAccountCursor(tx kv.RwTx, cursor []byte) error {
	return tx.Put(modules.SnapSyncProgress, keyAccountCursor, cursor)
}

// LoadAccountCursor returns the saved account cursor, or nil if not set.
func LoadAccountCursor(tx kv.Tx) ([]byte, error) {
	val, err := tx.GetOne(modules.SnapSyncProgress, keyAccountCursor)
	if err != nil || val == nil {
		return nil, err
	}
	result := make([]byte, len(val))
	copy(result, val)
	return result, nil
}

// SaveState persists the snap sync state (running/completed).
func SaveState(tx kv.RwTx, state string) error {
	return tx.Put(modules.SnapSyncProgress, keyState, []byte(state))
}

// LoadState returns the saved snap sync state, or empty string if not set.
func LoadState(tx kv.Tx) (string, error) {
	val, err := tx.GetOne(modules.SnapSyncProgress, keyState)
	if err != nil || val == nil {
		return "", err
	}
	return string(val), nil
}

// ClearProgress removes all snap sync progress data.
func ClearProgress(ctx context.Context, db kv.RwDB) error {
	return db.Update(ctx, func(tx kv.RwTx) error {
		for _, key := range [][]byte{keyPivotBlock, keyAccountCursor, keyState} {
			if err := tx.Delete(modules.SnapSyncProgress, key); err != nil {
				return err
			}
		}
		return nil
	})
}
