/*
   Copyright 2021 Erigon contributors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package mdbx

import (
	"fmt"

	"github.com/erigontech/mdbx-go/mdbx"
)

type MdbxDupSortCursor struct {
	*MdbxCursor
}

func (c *MdbxDupSortCursor) Internal() *mdbx.Cursor {
	return c.c
}

// DeleteExact - does delete
func (c *MdbxDupSortCursor) DeleteExact(k1, k2 []byte) error {
	_, err := c.getBoth(k1, k2)
	if err != nil { // if key not found, or found another one - then nothing to delete
		if mdbx.IsNotFound(err) {
			return nil
		}
		return err
	}
	return c.delCurrent()
}

func (c *MdbxDupSortCursor) SeekBothExact(key, value []byte) ([]byte, []byte, error) {
	v, err := c.getBoth(key, value)
	if err != nil {
		if mdbx.IsNotFound(err) {
			return nil, nil, nil
		}
		return []byte{}, nil, fmt.Errorf("in SeekBothExact: %w", err)
	}
	return key, v, nil
}

func (c *MdbxDupSortCursor) SeekBothRange(key, value []byte) ([]byte, error) {
	v, err := c.getBothRange(key, value)
	if err != nil {
		if mdbx.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("in SeekBothRange, table=%s: %w", c.bucketName, err)
	}
	return v, nil
}

func (c *MdbxDupSortCursor) FirstDup() ([]byte, error) {
	v, err := c.firstDup()
	if err != nil {
		if mdbx.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("in FirstDup: %w", err)
	}
	return v, nil
}

// NextDup - iterate only over duplicates of current key
func (c *MdbxDupSortCursor) NextDup() ([]byte, []byte, error) {
	k, v, err := c.nextDup()
	if err != nil {
		if mdbx.IsNotFound(err) {
			return nil, nil, nil
		}
		return []byte{}, nil, fmt.Errorf("in NextDup: %w", err)
	}
	return k, v, nil
}

// NextNoDup - iterate with skipping all duplicates
func (c *MdbxDupSortCursor) NextNoDup() ([]byte, []byte, error) {
	k, v, err := c.nextNoDup()
	if err != nil {
		if mdbx.IsNotFound(err) {
			return nil, nil, nil
		}
		return []byte{}, nil, fmt.Errorf("in NextNoDup: %w", err)
	}
	return k, v, nil
}

func (c *MdbxDupSortCursor) PrevDup() ([]byte, []byte, error) {
	k, v, err := c.prevDup()
	if err != nil {
		if mdbx.IsNotFound(err) {
			return nil, nil, nil
		}
		return []byte{}, nil, fmt.Errorf("in PrevDup: %w", err)
	}
	return k, v, nil
}

func (c *MdbxDupSortCursor) PrevNoDup() ([]byte, []byte, error) {
	k, v, err := c.prevNoDup()
	if err != nil {
		if mdbx.IsNotFound(err) {
			return nil, nil, nil
		}
		return []byte{}, nil, fmt.Errorf("in PrevNoDup: %w", err)
	}
	return k, v, nil
}

func (c *MdbxDupSortCursor) LastDup() ([]byte, error) {
	v, err := c.lastDup()
	if err != nil {
		if mdbx.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("in LastDup: %w", err)
	}
	return v, nil
}

func (c *MdbxDupSortCursor) Append(k []byte, v []byte) error {
	if err := c.c.Put(k, v, mdbx.Append|mdbx.AppendDup); err != nil {
		return fmt.Errorf("label: %s, in Append: bucket=%s, %w", c.tx.db.opts.label, c.bucketName, err)
	}
	return nil
}

func (c *MdbxDupSortCursor) AppendDup(k []byte, v []byte) error {
	if err := c.c.Put(k, v, mdbx.AppendDup); err != nil {
		return fmt.Errorf("label: %s, in AppendDup: bucket=%s, %w", c.tx.db.opts.label, c.bucketName, err)
	}
	return nil
}

func (c *MdbxDupSortCursor) PutNoDupData(k, v []byte) error {
	if err := c.c.Put(k, v, mdbx.NoDupData); err != nil {
		return fmt.Errorf("label: %s, in PutNoDupData: %w", c.tx.db.opts.label, err)
	}

	return nil
}

// DeleteCurrentDuplicates - delete all of the data items for the current key.
func (c *MdbxDupSortCursor) DeleteCurrentDuplicates() error {
	if err := c.delAllDupData(); err != nil {
		return fmt.Errorf("label: %s,in DeleteCurrentDuplicates: %w", c.tx.db.opts.label, err)
	}
	return nil
}

// CountDuplicates returns the number of duplicates for the current key. See mdb_cursor_count
func (c *MdbxDupSortCursor) CountDuplicates() (uint64, error) {
	res, err := c.c.Count()
	if err != nil {
		return 0, fmt.Errorf("in CountDuplicates: %w", err)
	}
	return res, nil
}
