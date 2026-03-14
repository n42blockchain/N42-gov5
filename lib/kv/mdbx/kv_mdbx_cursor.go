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
	"bytes"
	"fmt"

	"github.com/erigontech/mdbx-go/mdbx"

	"github.com/n42blockchain/N42/lib/kv"
)

type MdbxCursor struct {
	tx         *MdbxTx
	c          *mdbx.Cursor
	bucketName string
	bucketCfg  kv.TableCfgItem
	dbi        mdbx.DBI
	id         uint64
}

// methods here help to see better pprof picture
func (c *MdbxCursor) set(k []byte) ([]byte, []byte, error) { return c.c.Get(k, nil, mdbx.Set) }
func (c *MdbxCursor) getCurrent() ([]byte, []byte, error)  { return c.c.Get(nil, nil, mdbx.GetCurrent) }
func (c *MdbxCursor) first() ([]byte, []byte, error)       { return c.c.Get(nil, nil, mdbx.First) }
func (c *MdbxCursor) next() ([]byte, []byte, error)        { return c.c.Get(nil, nil, mdbx.Next) }
func (c *MdbxCursor) nextDup() ([]byte, []byte, error)     { return c.c.Get(nil, nil, mdbx.NextDup) }
func (c *MdbxCursor) nextNoDup() ([]byte, []byte, error)   { return c.c.Get(nil, nil, mdbx.NextNoDup) }
func (c *MdbxCursor) prev() ([]byte, []byte, error)        { return c.c.Get(nil, nil, mdbx.Prev) }
func (c *MdbxCursor) prevDup() ([]byte, []byte, error)     { return c.c.Get(nil, nil, mdbx.PrevDup) }
func (c *MdbxCursor) prevNoDup() ([]byte, []byte, error)   { return c.c.Get(nil, nil, mdbx.PrevNoDup) }
func (c *MdbxCursor) last() ([]byte, []byte, error)        { return c.c.Get(nil, nil, mdbx.Last) }
func (c *MdbxCursor) delCurrent() error                    { return c.c.Del(mdbx.Current) }
func (c *MdbxCursor) delAllDupData() error                 { return c.c.Del(mdbx.AllDups) }
func (c *MdbxCursor) put(k, v []byte) error                { return c.c.Put(k, v, 0) }
func (c *MdbxCursor) putCurrent(k, v []byte) error         { return c.c.Put(k, v, mdbx.Current) }
func (c *MdbxCursor) putNoOverwrite(k, v []byte) error     { return c.c.Put(k, v, mdbx.NoOverwrite) }
func (c *MdbxCursor) getBoth(k, v []byte) ([]byte, error) {
	_, v, err := c.c.Get(k, v, mdbx.GetBoth)
	return v, err
}
func (c *MdbxCursor) setRange(k []byte) ([]byte, []byte, error) {
	return c.c.Get(k, nil, mdbx.SetRange)
}
func (c *MdbxCursor) getBothRange(k, v []byte) ([]byte, error) {
	_, v, err := c.c.Get(k, v, mdbx.GetBothRange)
	return v, err
}
func (c *MdbxCursor) firstDup() ([]byte, error) {
	_, v, err := c.c.Get(nil, nil, mdbx.FirstDup)
	return v, err
}
func (c *MdbxCursor) lastDup() ([]byte, error) {
	_, v, err := c.c.Get(nil, nil, mdbx.LastDup)
	return v, err
}

// restoreDupSortKey reassembles the full key from a DupSort-shortened key
// by moving the key suffix from the beginning of the value back into the key.
func (c *MdbxCursor) restoreDupSortKey(k, v []byte) ([]byte, []byte) {
	b := c.bucketCfg
	if !b.AutoDupSortKeysConversion || len(k) != b.DupToLen {
		return k, v
	}
	keyPart := b.DupFromLen - b.DupToLen
	k = append(k, v[:keyPart]...)
	v = v[keyPart:]
	return k, v
}

func (c *MdbxCursor) Count() (uint64, error) {
	st, err := c.tx.tx.StatDBI(c.dbi)
	if err != nil {
		return 0, err
	}
	return st.Entries, nil
}

func (c *MdbxCursor) First() ([]byte, []byte, error) {
	return c.Seek(nil)
}

func (c *MdbxCursor) Last() ([]byte, []byte, error) {
	k, v, err := c.last()
	if err != nil {
		if mdbx.IsNotFound(err) {
			return nil, nil, nil
		}
		return []byte{}, nil, fmt.Errorf("failed MdbxKV cursor.Last(): %w, bucket: %s", err, c.bucketName)
	}

	k, v = c.restoreDupSortKey(k, v)
	return k, v, nil
}

func (c *MdbxCursor) Seek(seek []byte) (k, v []byte, err error) {
	if c.bucketCfg.AutoDupSortKeysConversion {
		return c.seekDupSort(seek)
	}

	if len(seek) == 0 {
		k, v, err = c.first()
	} else {
		k, v, err = c.setRange(seek)
	}
	if err != nil {
		if mdbx.IsNotFound(err) {
			return nil, nil, nil
		}
		err = fmt.Errorf("failed MdbxKV cursor.Seek(): %w, bucket: %s,  key: %x", err, c.bucketName, seek)
		return []byte{}, nil, err
	}

	return k, v, nil
}

func (c *MdbxCursor) seekDupSort(seek []byte) (k, v []byte, err error) {
	b := c.bucketCfg
	to := b.DupToLen

	if len(seek) == 0 {
		k, v, err = c.first()
		if err != nil {
			if mdbx.IsNotFound(err) {
				return nil, nil, nil
			}
			return []byte{}, nil, err
		}
		k, v = c.restoreDupSortKey(k, v)
		return k, v, nil
	}

	var seek1, seek2 []byte
	if len(seek) > to {
		seek1, seek2 = seek[:to], seek[to:]
	} else {
		seek1 = seek
	}
	k, v, err = c.setRange(seek1)
	if err != nil {
		if mdbx.IsNotFound(err) {
			return nil, nil, nil
		}
		return []byte{}, nil, err
	}

	if seek2 != nil && bytes.Equal(seek1, k) {
		v, err = c.getBothRange(seek1, seek2)
		if err != nil && mdbx.IsNotFound(err) {
			k, v, err = c.next()
			if err != nil {
				if mdbx.IsNotFound(err) {
					return nil, nil, nil
				}
				return []byte{}, nil, err
			}
		} else if err != nil {
			return []byte{}, nil, err
		}
	}

	k, v = c.restoreDupSortKey(k, v)
	return k, v, nil
}

func (c *MdbxCursor) Next() (k, v []byte, err error) {
	k, v, err = c.next()
	if err != nil {
		if mdbx.IsNotFound(err) {
			return nil, nil, nil
		}
		return []byte{}, nil, fmt.Errorf("failed MdbxKV cursor.Next(): %w", err)
	}

	b := c.bucketCfg
	if b.AutoDupSortKeysConversion && len(k) == b.DupToLen && len(v) == 0 {
		return nil, nil, fmt.Errorf("key with empty value: k=%x, len(k)=%d, v=%x", k, len(k), v)
	}
	k, v = c.restoreDupSortKey(k, v)
	return k, v, nil
}

func (c *MdbxCursor) Prev() (k, v []byte, err error) {
	k, v, err = c.prev()
	if err != nil {
		if mdbx.IsNotFound(err) {
			return nil, nil, nil
		}
		return []byte{}, nil, fmt.Errorf("failed MdbxKV cursor.Prev(): %w", err)
	}

	k, v = c.restoreDupSortKey(k, v)
	return k, v, nil
}

// Current returns the key/data at the current cursor position.
func (c *MdbxCursor) Current() ([]byte, []byte, error) {
	k, v, err := c.getCurrent()
	if err != nil {
		if mdbx.IsNotFound(err) {
			return nil, nil, nil
		}
		return []byte{}, nil, err
	}

	k, v = c.restoreDupSortKey(k, v)
	return k, v, nil
}

func (c *MdbxCursor) Delete(k []byte) error {
	if c.bucketCfg.AutoDupSortKeysConversion {
		return c.deleteDupSort(k)
	}

	_, _, err := c.set(k)
	if err != nil {
		if mdbx.IsNotFound(err) {
			return nil
		}
		return err
	}

	if c.bucketCfg.Flags&mdbx.DupSort != 0 {
		return c.delAllDupData()
	}
	return c.delCurrent()
}

// DeleteCurrent This function deletes the key/data pair to which the cursor refers.
// This does not invalidate the cursor, so operations such as MDB_NEXT
// can still be used on it.
// Both MDB_NEXT and MDB_GET_CURRENT will return the same record after
// this operation.
func (c *MdbxCursor) DeleteCurrent() error {
	return c.delCurrent()
}

func (c *MdbxCursor) deleteDupSort(key []byte) error {
	b := c.bucketCfg
	from, to := b.DupFromLen, b.DupToLen
	if len(key) != from && len(key) >= to {
		return fmt.Errorf("delete from dupsort bucket: %s, can have keys of len==%d and len<%d. key: %x,%d", c.bucketName, from, to, key, len(key))
	}

	if len(key) == from {
		v, err := c.getBothRange(key[:to], key[to:])
		if err != nil { // if key not found, or found another one - then nothing to delete
			if mdbx.IsNotFound(err) {
				return nil
			}
			return err
		}
		if !bytes.Equal(v[:from-to], key[to:]) {
			return nil
		}
		return c.delCurrent()
	}

	_, _, err := c.set(key)
	if err != nil {
		if mdbx.IsNotFound(err) {
			return nil
		}
		return err
	}

	return c.delCurrent()
}

func (c *MdbxCursor) PutNoOverwrite(key []byte, value []byte) error {
	if c.bucketCfg.AutoDupSortKeysConversion {
		return fmt.Errorf("PutNoOverwrite does not support AutoDupSortKeysConversion")
	}

	return c.putNoOverwrite(key, value)
}

func (c *MdbxCursor) Put(key []byte, value []byte) error {
	var err error
	if c.bucketCfg.AutoDupSortKeysConversion {
		err = c.putDupSort(key, value)
	} else {
		err = c.put(key, value)
	}
	if err != nil {
		return fmt.Errorf("label: %s, table: %s, %w", c.tx.db.opts.label, c.bucketName, err)
	}
	return nil
}

func (c *MdbxCursor) putDupSort(key []byte, value []byte) error {
	b := c.bucketCfg
	from, to := b.DupFromLen, b.DupToLen
	if len(key) != from && len(key) >= to {
		return fmt.Errorf("label: %s, table: %s, can have keys of len==%d and len<%d. key: %x,%d", c.tx.db.opts.label, c.bucketName, from, to, key, len(key))
	}

	if len(key) != from {
		err := c.putNoOverwrite(key, value)
		if err != nil {
			if mdbx.IsKeyExists(err) {
				return c.putCurrent(key, value)
			}
			return fmt.Errorf("label: %s, putNoOverwrite, bucket: %s, key: %x, val: %x, err: %w", c.tx.db.opts.label, c.bucketName, key, value, err)
		}
		return nil
	}

	value = append(key[to:], value...)
	key = key[:to]
	v, err := c.getBothRange(key, value[:from-to])
	if err != nil { // if key not found, or found another one - then just insert
		if mdbx.IsNotFound(err) {
			return c.put(key, value)
		}
		return err
	}

	if bytes.Equal(v[:from-to], value[:from-to]) {
		if len(v) == len(value) { // in DupSort case mdbx.Current works only with values of same length
			return c.putCurrent(key, value)
		}
		err = c.delCurrent()
		if err != nil {
			return err
		}
	}

	return c.put(key, value)
}

func (c *MdbxCursor) SeekExact(key []byte) ([]byte, []byte, error) {
	b := c.bucketCfg
	if b.AutoDupSortKeysConversion && len(key) == b.DupFromLen {
		from, to := b.DupFromLen, b.DupToLen
		v, err := c.getBothRange(key[:to], key[to:])
		if err != nil {
			if mdbx.IsNotFound(err) {
				return nil, nil, nil
			}
			return []byte{}, nil, err
		}
		if !bytes.Equal(key[to:], v[:from-to]) {
			return nil, nil, nil
		}
		return key[:to], v[from-to:], nil
	}

	k, v, err := c.set(key)
	if err != nil {
		if mdbx.IsNotFound(err) {
			return nil, nil, nil
		}
		return []byte{}, nil, err
	}
	return k, v, nil
}

// Append - speedy feature of mdbx which is not part of KV interface.
// Cast your cursor to *MdbxCursor to use this method.
// Return error - if provided data will not sorted (or bucket have old records which mess with new in sorting manner).
func (c *MdbxCursor) Append(k []byte, v []byte) error {
	if c.bucketCfg.AutoDupSortKeysConversion {
		b := c.bucketCfg
		from, to := b.DupFromLen, b.DupToLen
		if len(k) != from && len(k) >= to {
			return fmt.Errorf("label: %s, append dupsort bucket: %s, can have keys of len==%d and len<%d. key: %x,%d", c.tx.db.opts.label, c.bucketName, from, to, k, len(k))
		}

		if len(k) == from {
			v = append(k[to:], v...)
			k = k[:to]
		}
	}

	if c.bucketCfg.Flags&mdbx.DupSort != 0 {
		if err := c.c.Put(k, v, mdbx.AppendDup); err != nil {
			return fmt.Errorf("label: %s, bucket: %s, %w", c.tx.db.opts.label, c.bucketName, err)
		}
		return nil
	}

	if err := c.c.Put(k, v, mdbx.Append); err != nil {
		return fmt.Errorf("label: %s, bucket: %s, %w", c.tx.db.opts.label, c.bucketName, err)
	}
	return nil
}

func (c *MdbxCursor) Close() {
	if c.c != nil {
		c.c.Close()
		delete(c.tx.cursors, c.id)
		c.c = nil
	}
}
