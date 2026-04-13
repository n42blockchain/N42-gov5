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

package snapshot

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
)

// AccountEntry is one account returned by ReadAccountRange.
type AccountEntry struct {
	Address types.Address
	Data    []byte // protobuf-encoded StateAccount
}

// StorageEntry is one storage slot returned by ReadStorageRange.
type StorageEntry struct {
	Key   []byte // address(20) + storageKey(32)
	Value []byte
}

const (
	maxRangeBytes   = 512 * 1024 // 512 KB soft cap per range response
	maxRangeEntries = 4096
)

// ReadAccountRange reads account state as-of the given snapshot height.
// It returns up to maxRangeEntries accounts starting from `start` (inclusive).
// If start is zero-address, iteration begins at the table start.
// Returns entries, the next start key for pagination (nil if done), and error.
func ReadAccountRange(tx kv.Tx, snapshotHeight uint64, start types.Address, limit int) ([]AccountEntry, *types.Address, error) {
	if err := validateSnapshotExists(tx, snapshotHeight); err != nil {
		return nil, nil, err
	}

	if limit <= 0 || limit > maxRangeEntries {
		limit = maxRangeEntries
	}

	var entries []AccountEntry
	var totalBytes int
	var nextAddr *types.Address

	err := state.WalkAsOfAccounts(tx, start, snapshotHeight, func(k []byte, v []byte) (bool, error) {
		if len(k) != types.AddressLength {
			return true, nil // skip malformed
		}
		if len(entries) >= limit || totalBytes >= maxRangeBytes {
			addr := types.BytesToAddress(k)
			nextAddr = &addr
			return false, nil // stop
		}
		var entry AccountEntry
		entry.Address = types.BytesToAddress(k)
		entry.Data = make([]byte, len(v))
		copy(entry.Data, v)
		entries = append(entries, entry)
		totalBytes += len(k) + len(v)
		return true, nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walk accounts as-of %d: %w", snapshotHeight, err)
	}

	return entries, nextAddr, nil
}

// ReadStorageRange reads storage slots for a specific account as-of the snapshot height.
// Returns entries, next start key for pagination (nil if done), and error.
func ReadStorageRange(tx kv.Tx, snapshotHeight uint64, address types.Address, incarnation uint16, startLocation types.Hash, limit int) ([]StorageEntry, *types.Hash, error) {
	if err := validateSnapshotExists(tx, snapshotHeight); err != nil {
		return nil, nil, err
	}

	if limit <= 0 || limit > maxRangeEntries {
		limit = maxRangeEntries
	}

	var entries []StorageEntry
	var totalBytes int
	var nextLoc *types.Hash

	err := state.WalkAsOfStorage(tx, address, incarnation, startLocation, snapshotHeight, func(addr, loc, v []byte) (bool, error) {
		if len(entries) >= limit || totalBytes >= maxRangeBytes {
			hash := types.BytesToHash(loc)
			nextLoc = &hash
			return false, nil
		}
		// Build composite key: address(20) + loc(32)
		key := modules.PlainGenerateCompositeStorageKey(addr, loc)
		val := make([]byte, len(v))
		copy(val, v)
		entries = append(entries, StorageEntry{Key: key, Value: val})
		totalBytes += len(key) + len(v)
		return true, nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walk storage as-of %d for %x: %w", snapshotHeight, address, err)
	}

	return entries, nextLoc, nil
}

// ReadChangeSetRange reads account changesets in the block range [from, to).
// This enables incremental sync: a peer that already has state at `from`
// can apply these changesets to advance to `to`.
type ChangeSetEntry struct {
	BlockNumber uint64
	Address     []byte
	Data        []byte // old account value at that block
}

// ReadAccountChangeSets returns all account change entries in [from, to).
func ReadAccountChangeSets(tx kv.Tx, from, to uint64, limit int) ([]ChangeSetEntry, error) {
	if from >= to {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10000
	}

	c, err := tx.CursorDupSort(modules.AccountChangeSet)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	var entries []ChangeSetEntry
	startKey := make([]byte, 8)
	binary.BigEndian.PutUint64(startKey, from)

	for k, v, err := c.Seek(startKey); k != nil; k, v, err = c.NextDup() {
		if err != nil {
			return nil, err
		}
		blockNum := binary.BigEndian.Uint64(k[:8])
		if blockNum >= to {
			break
		}
		if len(entries) >= limit {
			break
		}
		// v = address(20) + account_data
		if len(v) < types.AddressLength {
			continue
		}
		entries = append(entries, ChangeSetEntry{
			BlockNumber: blockNum,
			Address:     v[:types.AddressLength],
			Data:        v[types.AddressLength:],
		})

		// When all dups for this key are exhausted, move to the next key.
		if k2, v2, err2 := c.NextNoDup(); err2 != nil {
			return nil, err2
		} else if k2 != nil {
			blockNum2 := binary.BigEndian.Uint64(k2[:8])
			if blockNum2 >= to || len(entries) >= limit {
				break
			}
			// Process first dup of the new key.
			if len(v2) >= types.AddressLength {
				entries = append(entries, ChangeSetEntry{
					BlockNumber: blockNum2,
					Address:     v2[:types.AddressLength],
					Data:        v2[types.AddressLength:],
				})
			}
		} else {
			break
		}
	}

	return entries, nil
}

// validateSnapshotExists checks that a snapshot at the given height is registered.
func validateSnapshotExists(tx kv.Tx, height uint64) error {
	m, err := LoadSnapshot(tx, height)
	if err != nil {
		return fmt.Errorf("load snapshot at %d: %w", height, err)
	}
	if m == nil {
		return errors.New("snapshot not found at requested height")
	}
	return nil
}
