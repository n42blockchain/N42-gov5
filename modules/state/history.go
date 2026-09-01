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
// Historical state lookups over changeset history buckets.
// GetAsOf fetches the value of an Account or Storage key as of a
// given block timestamp, first trying FindByHistory over the
// AccountChangeSet/StorageChangeSet cursors and falling back to the
// live value in modules.Account/Storage on ErrKeyNotFound.
// Uses changeset.Mapper IndexChunkKey to navigate history chunks.

package state

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/RoaringBitmap/roaring/roaring64"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/changeset"
	"github.com/n42blockchain/N42/modules/ethdb"
	"github.com/n42blockchain/N42/modules/ethdb/bitmapdb"
)

func GetAsOf(tx kv.Tx, indexC kv.Cursor, changesC kv.CursorDupSort, storage bool, key []byte, timestamp uint64) ([]byte, error) {
	v, err := FindByHistory(tx, indexC, changesC, storage, key, timestamp)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, ethdb.ErrKeyNotFound) {
		return nil, err
	}
	if storage {
		return tx.GetOne(modules.Storage, key)
	}
	return tx.GetOne(modules.Account, key)
}

func FindByHistory(tx kv.Tx, indexC kv.Cursor, changesC kv.CursorDupSort, storage bool, key []byte, timestamp uint64) ([]byte, error) {
	var csBucket string
	if storage {
		csBucket = modules.StorageChangeSet
	} else {
		csBucket = modules.AccountChangeSet
	}

	k, v, seekErr := indexC.Seek(changeset.Mapper[csBucket].IndexChunkKey(key, timestamp))
	if seekErr != nil {
		return nil, seekErr
	}

	if k == nil {
		return nil, ethdb.ErrKeyNotFound
	}
	if storage {
		// Storage key = addr(20) + slot(32) = 52B.
		if !bytes.Equal(k[:types.AddressLength], key[:types.AddressLength]) ||
			!bytes.Equal(k[types.AddressLength:types.AddressLength+types.HashLength], key[types.AddressLength:]) {
			return nil, ethdb.ErrKeyNotFound
		}
	} else {
		if !bytes.HasPrefix(k, key) {
			return nil, ethdb.ErrKeyNotFound
		}
	}
	index := roaring64.New()
	if _, err := index.ReadFrom(bytes.NewReader(v)); err != nil {
		return nil, err
	}
	found, ok := bitmapdb.SeekInBitmap64(index, timestamp)
	changeSetBlock := found

	var data []byte
	var err error
	if ok {
		data, err = changeset.Mapper[csBucket].Find(changesC, changeSetBlock, key)
		if err != nil {
			if !errors.Is(err, changeset.ErrNotFound) {
				return nil, fmt.Errorf("finding %x in the changeset %d: %w", key, changeSetBlock, err)
			}
			return nil, ethdb.ErrKeyNotFound
		}
	} else {
		return nil, ethdb.ErrKeyNotFound
	}

	// Phase B: account changeset OldValues are self-contained (full V2 with
	// CodeHash inline); no historical CodeHash recovery is needed.
	return data, nil
}

// WalkAsOfStorage walks the storage of one account as of a historical block.
// Storage keys are address(20) + slot(32) = 52B.
func WalkAsOfStorage(tx kv.Tx, address types.Address, startLocation types.Hash, timestamp uint64, walker func(k1, k2, v []byte) (bool, error)) error {
	var startkey = make([]byte, types.AddressLength+types.HashLength)
	copy(startkey, address.Bytes())
	copy(startkey[types.AddressLength:], startLocation.Bytes())

	// Main cursor for current storage state.
	mCursor, err := tx.Cursor(modules.Storage)
	if err != nil {
		return err
	}
	defer mCursor.Close()
	mainCursor := ethdb.NewSplitCursor(
		mCursor,
		startkey,
		8*types.AddressLength,
		types.AddressLength,                  /* part1end */
		types.AddressLength,                  /* part2start */
		types.AddressLength+types.HashLength, /* part3start */
	)

	// History cursor for historic data.
	shCursor, err := tx.Cursor(modules.StorageHistory)
	if err != nil {
		return err
	}
	defer shCursor.Close()
	var hCursor = ethdb.NewSplitCursor(
		shCursor,
		startkey,
		8*types.AddressLength,
		types.AddressLength,                  /* part1end */
		types.AddressLength,                  /* part2start */
		types.AddressLength+types.HashLength, /* part3start */
	)
	csCursor, err := tx.CursorDupSort(modules.StorageChangeSet)
	if err != nil {
		return err
	}
	defer csCursor.Close()

	addr, loc, _, v, err1 := mainCursor.Seek()
	if err1 != nil {
		return err1
	}

	hAddr, hLoc, tsEnc, hV, err2 := hCursor.Seek()
	if err2 != nil {
		return err2
	}
	for hLoc != nil && binary.BigEndian.Uint64(tsEnc) < timestamp {
		if hAddr, hLoc, tsEnc, hV, err2 = hCursor.Next(); err2 != nil {
			return err2
		}
	}
	goOn := true
	for goOn {
		cmp, br := types.KeyCmp(addr, hAddr)
		if br {
			break
		}
		if cmp == 0 {
			cmp, br = types.KeyCmp(loc, hLoc)
		}
		if br {
			break
		}

		// Next key in current state is before the history cursor.
		if cmp < 0 {
			goOn, err = walker(addr, loc, v)
		} else {
			index := roaring64.New()
			if _, err = index.ReadFrom(bytes.NewReader(hV)); err != nil {
				return err
			}
			found, ok := bitmapdb.SeekInBitmap64(index, timestamp)
			changeSetBlock := found

			if ok {
				// Extract value from the changeSet.
				// StorageChangeSet key = blockNum(8) + addr(20) = 28B.
				csKey := make([]byte, 8+types.AddressLength)
				copy(csKey, modules.EncodeBlockNumber(changeSetBlock))
				copy(csKey[8:], address[:])
				kData := csKey
				data, err3 := csCursor.SeekBothRange(csKey, hLoc)
				if err3 != nil {
					return err3
				}
				if !bytes.Equal(kData, csKey) || !bytes.HasPrefix(data, hLoc) {
					return fmt.Errorf("inconsistent storage changeset and history kData %x, csKey %x, data %x, hLoc %x", kData, csKey, data, hLoc)
				}
				data = data[types.HashLength:]
				if len(data) > 0 { // Skip deleted entries
					goOn, err = walker(hAddr, hLoc, data)
				}
			} else if cmp == 0 {
				goOn, err = walker(addr, loc, v)
			}
		}
		if err != nil {
			return err
		}
		if goOn {
			if cmp <= 0 {
				if addr, loc, _, v, err1 = mainCursor.Next(); err1 != nil {
					return err1
				}
			}
			if cmp >= 0 {
				hLoc0 := hLoc
				for hLoc != nil && (bytes.Equal(hLoc0, hLoc) || binary.BigEndian.Uint64(tsEnc) < timestamp) {
					if hAddr, hLoc, tsEnc, hV, err2 = hCursor.Next(); err2 != nil {
						return err2
					}
				}
			}
		}
	}
	return nil
}

func WalkAsOfAccounts(tx kv.Tx, startAddress types.Address, timestamp uint64, walker func(k []byte, v []byte) (bool, error)) error {
	mainCursor, err := tx.Cursor(modules.Account)
	if err != nil {
		return err
	}
	defer mainCursor.Close()
	ahCursor, err := tx.Cursor(modules.AccountsHistory)
	if err != nil {
		return err
	}
	defer ahCursor.Close()
	var hCursor = ethdb.NewSplitCursor(
		ahCursor,
		startAddress.Bytes(),
		0,                     /* fixedBits */
		types.AddressLength,   /* part1end */
		types.AddressLength,   /* part2start */
		types.AddressLength+8, /* part3start */
	)
	csCursor, err := tx.CursorDupSort(modules.AccountChangeSet)
	if err != nil {
		return err
	}
	defer csCursor.Close()

	k, v, err1 := mainCursor.Seek(startAddress.Bytes())
	if err1 != nil {
		return err1
	}
	for k != nil && len(k) > types.AddressLength {
		k, v, err1 = mainCursor.Next()
		if err1 != nil {
			return err1
		}
	}
	hK, tsEnc, _, hV, err2 := hCursor.Seek()
	if err2 != nil {
		return err2
	}
	for hK != nil && binary.BigEndian.Uint64(tsEnc) < timestamp {
		hK, tsEnc, _, hV, err2 = hCursor.Next()
		if err2 != nil {
			return err2
		}
	}

	goOn := true
	for goOn {
		// Compare current state key with history key to determine merge order.
		cmp, br := types.KeyCmp(k, hK)
		if br {
			break
		}
		if cmp < 0 {
			goOn, err = walker(k, v)
		} else {
			index := roaring64.New()
			_, err = index.ReadFrom(bytes.NewReader(hV))
			if err != nil {
				return err
			}
			found, ok := bitmapdb.SeekInBitmap64(index, timestamp)
			changeSetBlock := found
			if ok {
				// Extract value from the changeSet
				csKey := modules.EncodeBlockNumber(changeSetBlock)
				kData := csKey
				data, err3 := csCursor.SeekBothRange(csKey, hK)
				if err3 != nil {
					return err3
				}
				if !bytes.Equal(kData, csKey) || !bytes.HasPrefix(data, hK) {
					return fmt.Errorf("inconsistent account history and changesets, kData %x, csKey %x, data %x, hK %x", kData, csKey, data, hK)
				}
				data = data[types.AddressLength:]
				if len(data) > 0 { // Skip accounts did not exist
					goOn, err = walker(hK, data)
				}
			} else if cmp == 0 {
				goOn, err = walker(k, v)
			}
		}
		if err != nil {
			return err
		}
		if goOn {
			if cmp <= 0 {
				k, v, err1 = mainCursor.Next()
				if err1 != nil {
					return err1
				}
				for k != nil && len(k) > types.AddressLength {
					k, v, err1 = mainCursor.Next()
					if err1 != nil {
						return err1
					}
				}
			}
			if cmp >= 0 {
				hK0 := hK
				for hK != nil && (bytes.Equal(hK0, hK) || binary.BigEndian.Uint64(tsEnc) < timestamp) {
					hK, tsEnc, _, hV, err1 = hCursor.Next()
					if err1 != nil {
						return err1
					}
				}
			}
		}
	}
	return err

}
