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
// Core ChangeSet type for account/storage history tracking.
// ChangeSet stores a sorted list of Change{Key,Value} records with a
// fixed keyLen invariant and implements sort.Interface for deterministic
// ordering before encoding. Used by account_changeset.go and
// storage_changeset.go to serialize per-block history entries into
// MDBX history buckets via Encoder/Decoder function pairs.

package changeset

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/ethdb"
)

func NewChangeSet() *ChangeSet {
	return &ChangeSet{
		Changes: make([]Change, 0),
	}
}

type Change struct {
	Key   []byte
	Value []byte
}

// ChangeSet is a map with keys of the same size.
// Both keys and values are byte strings.
type ChangeSet struct {
	// Invariant: all keys are of the same size.
	Changes []Change
	keyLen  int
}

// sort.Interface implementation

func (s *ChangeSet) Len() int {
	return len(s.Changes)
}

func (s *ChangeSet) Swap(i, j int) {
	s.Changes[i], s.Changes[j] = s.Changes[j], s.Changes[i]
}

func (s *ChangeSet) Less(i, j int) bool {
	cmp := bytes.Compare(s.Changes[i].Key, s.Changes[j].Key)
	if cmp == 0 {
		cmp = bytes.Compare(s.Changes[i].Value, s.Changes[j].Value)
	}
	return cmp < 0
}

func (s *ChangeSet) KeySize() int {
	if s.keyLen != 0 {
		return s.keyLen
	}
	if len(s.Changes) > 0 {
		return len(s.Changes[0].Key)
	}
	return 0
}

func (s *ChangeSet) checkKeySize(key []byte) error {
	if s.Len() == 0 && s.KeySize() == 0 {
		return nil
	}
	if len(key) > 0 && len(key) == s.KeySize() {
		return nil
	}
	return fmt.Errorf("wrong key size in ChangeSet: expected %d, actual %d", s.KeySize(), len(key))
}

// Add appends a new entry to the ChangeSet.
// All keys must be the same size; adding an existing key is not allowed.
func (s *ChangeSet) Add(key, value []byte) error {
	if err := s.checkKeySize(key); err != nil {
		return err
	}

	s.Changes = append(s.Changes, Change{
		Key:   key,
		Value: value,
	})
	return nil
}

func (s *ChangeSet) ChangedKeys() map[string]struct{} {
	m := make(map[string]struct{}, len(s.Changes))
	for i := range s.Changes {
		m[string(s.Changes[i].Key)] = struct{}{}
	}
	return m
}

func (s *ChangeSet) Equals(s2 *ChangeSet) bool {
	return reflect.DeepEqual(s.Changes, s2.Changes)
}

func (s *ChangeSet) String() string {
	var b strings.Builder
	for _, c := range s.Changes {
		fmt.Fprintf(&b, "%d %s : %s\n", len(c.Key), types.Bytes2Hex(c.Key), string(c.Value))
	}
	return b.String()
}

// FromDBFormat decodes a changeset entry from its database representation.
// Account keys are 8 bytes (block number only); storage keys are longer.
func FromDBFormat(dbKey, dbValue []byte) (uint64, []byte, []byte, error) {
	if len(dbKey) == 8 {
		return DecodeAccounts(dbKey, dbValue)
	}
	return DecodeStorage(dbKey, dbValue)
}

// AvailableFrom returns the earliest block number in the account changeset.
func AvailableFrom(tx kv.Tx) (uint64, error) {
	return availableFrom(tx, modules.AccountChangeSet)
}

// AvailableStorageFrom returns the earliest block number in the storage changeset.
func AvailableStorageFrom(tx kv.Tx) (uint64, error) {
	return availableFrom(tx, modules.StorageChangeSet)
}

func availableFrom(tx kv.Tx, bucket string) (uint64, error) {
	c, err := tx.Cursor(bucket)
	if err != nil {
		return math.MaxUint64, err
	}
	defer c.Close()
	k, _, err := c.First()
	if err != nil {
		return math.MaxUint64, err
	}
	if len(k) == 0 {
		return math.MaxUint64, nil
	}
	return binary.BigEndian.Uint64(k), nil
}

// ForRange iterates over changeset entries in the half-open block range [from, to).
func ForRange(db kv.Tx, bucket string, from, to uint64, walker func(blockN uint64, k, v []byte) error) error {
	c, err := db.Cursor(bucket)
	if err != nil {
		return err
	}
	defer c.Close()
	return ethdb.Walk(c, modules.EncodeBlockNumber(from), 0, func(k, v []byte) (bool, error) {
		blockN, k, v, err := FromDBFormat(k, v)
		if err != nil {
			return false, err
		}
		if blockN >= to {
			return false, nil
		}
		if err = walker(blockN, k, v); err != nil {
			return false, err
		}
		return true, nil
	})
}

// ForEach iterates over all changeset entries starting from startkey.
func ForEach(db kv.Tx, bucket string, startkey []byte, walker func(blockN uint64, k, v []byte) error) error {
	return db.ForEach(bucket, startkey, func(k, v []byte) error {
		blockN, k, v, err := FromDBFormat(k, v)
		if err != nil {
			return err
		}
		return walker(blockN, k, v)
	})
}

// ForPrefix iterates over changeset entries matching a key prefix.
func ForPrefix(db kv.Tx, bucket string, startkey []byte, walker func(blockN uint64, k, v []byte) error) error {
	return db.ForPrefix(bucket, startkey, func(k, v []byte) error {
		blockN, k, v, err := FromDBFormat(k, v)
		if err != nil {
			return err
		}
		return walker(blockN, k, v)
	})
}

// Truncate removes all changeset entries at or after the given block number.
func Truncate(tx kv.RwTx, from uint64) error {
	keyStart := modules.EncodeBlockNumber(from)
	if err := truncateBucket(tx, modules.AccountChangeSet, keyStart); err != nil {
		return err
	}
	return truncateBucket(tx, modules.StorageChangeSet, keyStart)
}

func truncateBucket(tx kv.RwTx, bucket string, keyStart []byte) error {
	c, err := tx.RwCursorDupSort(bucket)
	if err != nil {
		return err
	}
	defer c.Close()
	for k, _, err := c.Seek(keyStart); k != nil; k, _, err = c.NextNoDup() {
		if err != nil {
			return err
		}
		if err = c.DeleteCurrentDuplicates(); err != nil {
			return err
		}
	}
	return nil
}

var Mapper = map[string]struct {
	IndexBucket   string
	IndexChunkKey func([]byte, uint64) []byte
	Find          func(cursor kv.CursorDupSort, blockNumber uint64, key []byte) ([]byte, error)
	New           func() *ChangeSet
	Encode        Encoder
	Decode        Decoder
}{
	modules.AccountChangeSet: {
		IndexBucket:   modules.AccountsHistory,
		IndexChunkKey: modules.AccountIndexChunkKey,
		New:           NewAccountChangeSet,
		Find:          FindAccount,
		Encode:        EncodeAccounts,
		Decode:        DecodeAccounts,
	},
	modules.StorageChangeSet: {
		IndexBucket:   modules.StorageHistory,
		IndexChunkKey: modules.StorageIndexChunkKey,
		Find:          FindStorage,
		New:           NewStorageChangeSet,
		Encode:        EncodeStorage,
		Decode:        DecodeStorage,
	},
}
