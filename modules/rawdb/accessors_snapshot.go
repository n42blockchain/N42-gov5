// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// Snapshot metadata keys.
var (
	snapshotDiskRootKey   = []byte("disk_root")
	snapshotGenMarkerKey  = []byte("gen_marker")
	snapshotGenCompleteKey = []byte("gen_complete")
)

// --- SnapshotAccount CRUD ---

func WriteSnapshotAccount(tx kv.RwTx, address types.Address, data []byte) error {
	return tx.Put(modules.SnapshotAccount, address.Bytes(), data)
}

func ReadSnapshotAccount(tx kv.Tx, address types.Address) ([]byte, error) {
	return tx.GetOne(modules.SnapshotAccount, address.Bytes())
}

func DeleteSnapshotAccount(tx kv.RwTx, address types.Address) error {
	return tx.Delete(modules.SnapshotAccount, address.Bytes())
}

// --- SnapshotStorage CRUD ---

func WriteSnapshotStorage(tx kv.RwTx, compositeKey []byte, value []byte) error {
	return tx.Put(modules.SnapshotStorage, compositeKey, value)
}

func ReadSnapshotStorage(tx kv.Tx, compositeKey []byte) ([]byte, error) {
	return tx.GetOne(modules.SnapshotStorage, compositeKey)
}

func DeleteSnapshotStorage(tx kv.RwTx, compositeKey []byte) error {
	return tx.Delete(modules.SnapshotStorage, compositeKey)
}

// DeleteSnapshotStorageByAddress removes all storage entries for the given
// address prefix from the SnapshotStorage table.
func DeleteSnapshotStorageByAddress(tx kv.RwTx, address types.Address) error {
	c, err := tx.Cursor(modules.SnapshotStorage)
	if err != nil {
		return err
	}
	defer c.Close()

	prefix := address.Bytes()
	for k, _, err := c.Seek(prefix); k != nil; k, _, err = c.Next() {
		if err != nil {
			return err
		}
		// Keys are address(20)+incarnation(2)+key(32)=54 bytes.
		// Stop when address prefix no longer matches.
		if len(k) < types.AddressLength || !bytesEqual(k[:types.AddressLength], prefix) {
			break
		}
		if err := tx.Delete(modules.SnapshotStorage, k); err != nil {
			return err
		}
	}
	return nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- SnapshotMeta ---

// WriteSnapshotDiskRoot persists the disk layer's root hash and block number.
func WriteSnapshotDiskRoot(tx kv.RwTx, root types.Hash, blockNum uint64) error {
	val := make([]byte, 40) // hash(32) + blockNum(8)
	copy(val[:32], root[:])
	binary.BigEndian.PutUint64(val[32:], blockNum)
	return tx.Put(modules.SnapshotMeta, snapshotDiskRootKey, val)
}

// ReadSnapshotDiskRoot reads the persisted disk layer root and block number.
// Returns zero values if not set.
func ReadSnapshotDiskRoot(tx kv.Tx) (types.Hash, uint64, error) {
	val, err := tx.GetOne(modules.SnapshotMeta, snapshotDiskRootKey)
	if err != nil {
		return types.Hash{}, 0, err
	}
	if val == nil || len(val) < 40 {
		return types.Hash{}, 0, nil
	}
	var root types.Hash
	copy(root[:], val[:32])
	blockNum := binary.BigEndian.Uint64(val[32:])
	return root, blockNum, nil
}

// WriteSnapshotGenMarker persists the generation cursor (last processed address).
func WriteSnapshotGenMarker(tx kv.RwTx, marker []byte) error {
	return tx.Put(modules.SnapshotMeta, snapshotGenMarkerKey, marker)
}

// ReadSnapshotGenMarker reads the generation cursor.
// Returns nil, false if no marker is set.
func ReadSnapshotGenMarker(tx kv.Tx) ([]byte, bool, error) {
	val, err := tx.GetOne(modules.SnapshotMeta, snapshotGenMarkerKey)
	if err != nil {
		return nil, false, err
	}
	if val == nil {
		return nil, false, nil
	}
	return val, true, nil
}

// SetSnapshotGenComplete marks snapshot generation as complete.
func SetSnapshotGenComplete(tx kv.RwTx) error {
	return tx.Put(modules.SnapshotMeta, snapshotGenCompleteKey, []byte{0x01})
}

// IsSnapshotGenComplete checks if snapshot generation has completed.
func IsSnapshotGenComplete(tx kv.Tx) (bool, error) {
	val, err := tx.GetOne(modules.SnapshotMeta, snapshotGenCompleteKey)
	if err != nil {
		return false, err
	}
	return len(val) > 0 && val[0] == 0x01, nil
}

// ClearSnapshotGenState removes all generation state (marker + complete flag).
func ClearSnapshotGenState(tx kv.RwTx) error {
	if err := tx.Delete(modules.SnapshotMeta, snapshotGenMarkerKey); err != nil {
		return err
	}
	return tx.Delete(modules.SnapshotMeta, snapshotGenCompleteKey)
}

// --- SnapshotJournal ---

// WriteSnapshotJournal persists a serialized diff layer for crash recovery.
func WriteSnapshotJournal(tx kv.RwTx, blockNum uint64, data []byte) error {
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, blockNum)
	return tx.Put(modules.SnapshotJournal, key, data)
}

// ReadSnapshotJournalEntry reads a single journal entry by block number.
func ReadSnapshotJournalEntry(tx kv.Tx, blockNum uint64) ([]byte, error) {
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, blockNum)
	return tx.GetOne(modules.SnapshotJournal, key)
}

// ReadAllSnapshotJournal reads all journal entries in block number order.
func ReadAllSnapshotJournal(tx kv.Tx) ([]JournalEntry, error) {
	var entries []JournalEntry
	if err := tx.ForEach(modules.SnapshotJournal, nil, func(k, v []byte) error {
		if len(k) < 8 {
			return fmt.Errorf("snapshot: invalid journal key length %d", len(k))
		}
		blockNum := binary.BigEndian.Uint64(k)
		data := make([]byte, len(v))
		copy(data, v)
		entries = append(entries, JournalEntry{BlockNum: blockNum, Data: data})
		return nil
	}); err != nil {
		return nil, err
	}
	return entries, nil
}

// JournalEntry holds a single serialized diff layer for recovery.
type JournalEntry struct {
	BlockNum uint64
	Data     []byte
}

// ClearSnapshotJournal removes all journal entries.
func ClearSnapshotJournal(tx kv.RwTx) error {
	return tx.ClearBucket(modules.SnapshotJournal)
}

// DeleteSnapshotJournalEntry removes a single journal entry by block number.
func DeleteSnapshotJournalEntry(tx kv.RwTx, blockNum uint64) error {
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, blockNum)
	return tx.Delete(modules.SnapshotJournal, key)
}
