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

package changeset

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"

	libcommon "github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/common/length"
	"github.com/n42blockchain/N42/lib/etl"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)


var ErrNotFound = errors.New("not found")

func NewStorageChangeSet() *ChangeSet {
	return &ChangeSet{
		Changes: make([]Change, 0),
		keyLen:  length.Addr + length.Hash, // 52B: addr(20) + slot(32), no incarnation
	}
}

func EncodeStorage(blockN uint64, s *ChangeSet, f func(k, v []byte) error) error {
	sort.Sort(s)
	for _, cs := range s.Changes {
		// key = blockNum(8) + addr(20), value = slot(32) + oldValue
		newK := make([]byte, length.BlockNum+length.Addr)
		binary.BigEndian.PutUint64(newK, blockN)
		copy(newK[length.BlockNum:], cs.Key[:length.Addr])
		newV := make([]byte, 0, length.Hash+len(cs.Value))
		newV = append(append(newV, cs.Key[length.Addr:]...), cs.Value...)
		if err := f(newK, newV); err != nil {
			return err
		}
	}
	return nil
}

func DecodeStorage(dbKey, dbValue []byte) (uint64, []byte, []byte, error) {
	blockN := binary.BigEndian.Uint64(dbKey)
	if len(dbValue) < length.Hash {
		return 0, nil, nil, fmt.Errorf("storage changes purged for block %d", blockN)
	}
	// key = addr(20) + slot(32) = 52B
	k := make([]byte, length.Addr+length.Hash)
	dbKey = dbKey[length.BlockNum:]
	copy(k, dbKey)
	copy(k[len(dbKey):], dbValue[:length.Hash])
	v := dbValue[length.Hash:]
	if len(v) == 0 {
		v = nil
	}
	return blockN, k, v, nil
}

func FindStorage(c kv.CursorDupSort, blockNumber uint64, k []byte) ([]byte, error) {
	addr, loc := k[:length.Addr], k[length.Addr:]

	seek := make([]byte, length.BlockNum+length.Addr)
	binary.BigEndian.PutUint64(seek, blockNumber)
	copy(seek[length.BlockNum:], addr)

	v, err := c.SeekBothRange(seek, loc)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(v, loc) {
		return nil, ErrNotFound
	}
	return v[length.Hash:], nil
}

// RewindData collects changeset entries for rewinding state from timestampSrc back to timestampDst.
// It gathers entries in the half-open range (timestampDst, timestampSrc].
func RewindData(db kv.Tx, timestampSrc, timestampDst uint64, changes *etl.Collector, quit <-chan struct{}) error {
	from := timestampDst + 1
	to := timestampSrc + 1
	if err := walkAndCollect(changes.Collect, db, modules.AccountChangeSet, from, to, quit); err != nil {
		return err
	}
	return walkAndCollect(changes.Collect, db, modules.StorageChangeSet, from, to, quit)
}

func walkAndCollect(collect func([]byte, []byte) error, db kv.Tx, bucket string, from, to uint64, quit <-chan struct{}) error {
	return ForRange(db, bucket, from, to, func(_ uint64, k, v []byte) error {
		if err := libcommon.Stopped(quit); err != nil {
			return err
		}
		return collect(libcommon.Copy(k), libcommon.Copy(v))
	})
}
