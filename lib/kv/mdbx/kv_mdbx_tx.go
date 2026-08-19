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
	"encoding/binary"
	"fmt"

	"github.com/erigontech/mdbx-go/mdbx"

	"github.com/n42blockchain/N42/lib/common/dbg"
	"github.com/n42blockchain/N42/lib/kv"
)

func (tx *MdbxTx) statelessCursor(bucket string) (kv.RwCursor, error) {
	if tx.statelessCursors == nil {
		tx.statelessCursors = make(map[string]kv.RwCursor)
	}
	c, ok := tx.statelessCursors[bucket]
	if !ok {
		var err error
		c, err = tx.RwCursor(bucket)
		if err != nil {
			return nil, err
		}
		tx.statelessCursors[bucket] = c
	}
	return c, nil
}

func (tx *MdbxTx) GetOne(bucket string, k []byte) ([]byte, error) {
	c, err := tx.statelessCursor(bucket)
	if err != nil {
		return nil, err
	}
	_, v, err := c.SeekExact(k)
	if err == nil {
		tx.readCount.Add(1)
		tx.readBytes.Add(uint64(len(v)))
	}
	return v, err
}

func (tx *MdbxTx) Has(bucket string, key []byte) (bool, error) {
	c, err := tx.statelessCursor(bucket)
	if err != nil {
		return false, err
	}
	k, _, err := c.Seek(key)
	if err != nil {
		return false, err
	}
	return bytes.Equal(key, k), nil
}

func (tx *MdbxTx) Put(table string, k, v []byte) error {
	c, err := tx.statelessCursor(table)
	if err != nil {
		return err
	}
	if err := c.Put(k, v); err != nil {
		return err
	}
	tx.writeCount.Add(1)
	tx.writeBytes.Add(uint64(len(k) + len(v)))
	tx.noteWrite(table, len(k)+len(v), false)
	return nil
}

func (tx *MdbxTx) Delete(table string, k []byte) error {
	c, err := tx.statelessCursor(table)
	if err != nil {
		return err
	}
	if err := c.Delete(k); err != nil {
		return err
	}
	tx.writeCount.Add(1)
	tx.noteWrite(table, 0, true)
	return nil
}

func (tx *MdbxTx) Append(bucket string, k, v []byte) error {
	c, err := tx.statelessCursor(bucket)
	if err != nil {
		return err
	}
	if err := c.Append(k, v); err != nil {
		return err
	}
	tx.writeCount.Add(1)
	tx.writeBytes.Add(uint64(len(k) + len(v)))
	tx.noteWrite(bucket, len(k)+len(v), false)
	return nil
}
func (tx *MdbxTx) AppendDup(bucket string, k, v []byte) error {
	c, err := tx.statelessCursor(bucket)
	if err != nil {
		return err
	}
	if err := c.(*MdbxDupSortCursor).AppendDup(k, v); err != nil {
		return err
	}
	tx.writeCount.Add(1)
	tx.writeBytes.Add(uint64(len(k) + len(v)))
	tx.noteWrite(bucket, len(k)+len(v), false)
	return nil
}

func (tx *MdbxTx) IncrementSequence(bucket string, amount uint64) (uint64, error) {
	c, err := tx.statelessCursor(kv.Sequence)
	if err != nil {
		return 0, err
	}
	_, v, err := c.SeekExact([]byte(bucket))
	if err != nil {
		return 0, err
	}

	var currentV uint64
	if len(v) > 0 {
		currentV = binary.BigEndian.Uint64(v)
	}

	var newVBytes [8]byte
	binary.BigEndian.PutUint64(newVBytes[:], currentV+amount)
	err = c.Put([]byte(bucket), newVBytes[:])
	if err != nil {
		return 0, err
	}
	return currentV, nil
}

func (tx *MdbxTx) ReadSequence(bucket string) (uint64, error) {
	c, err := tx.statelessCursor(kv.Sequence)
	if err != nil {
		return 0, err
	}
	_, v, err := c.SeekExact([]byte(bucket))
	if err != nil && !mdbx.IsNotFound(err) {
		return 0, err
	}

	var currentV uint64
	if len(v) > 0 {
		currentV = binary.BigEndian.Uint64(v)
	}

	return currentV, nil
}

func (tx *MdbxTx) RwCursor(bucket string) (kv.RwCursor, error) {
	b := tx.db.buckets[bucket]
	if b.AutoDupSortKeysConversion {
		return tx.stdCursor(bucket)
	}

	if b.Flags&kv.DupSort != 0 {
		return tx.RwCursorDupSort(bucket)
	}

	return tx.stdCursor(bucket)
}

func (tx *MdbxTx) Cursor(bucket string) (kv.Cursor, error) {
	return tx.RwCursor(bucket)
}

func (tx *MdbxTx) stdCursor(bucket string) (kv.RwCursor, error) {
	b := tx.db.buckets[bucket]
	c := &MdbxCursor{bucketName: bucket, tx: tx, bucketCfg: b, dbi: mdbx.DBI(tx.db.buckets[bucket].DBI), id: tx.cursorID}
	tx.cursorID++

	var err error
	c.c, err = tx.tx.OpenCursor(c.dbi)
	if err != nil {
		return nil, fmt.Errorf("table: %s, %w, stack: %s", c.bucketName, err, dbg.Stack())
	}

	// add to auto-cleanup on end of transactions
	if tx.cursors == nil {
		tx.cursors = map[uint64]*mdbx.Cursor{}
	}
	tx.cursors[c.id] = c.c
	return c, nil
}

func (tx *MdbxTx) RwCursorDupSort(bucket string) (kv.RwCursorDupSort, error) {
	basicCursor, err := tx.stdCursor(bucket)
	if err != nil {
		return nil, err
	}
	return &MdbxDupSortCursor{MdbxCursor: basicCursor.(*MdbxCursor)}, nil
}

func (tx *MdbxTx) CursorDupSort(bucket string) (kv.CursorDupSort, error) {
	return tx.RwCursorDupSort(bucket)
}

func (tx *MdbxTx) CollectMetrics() {
	if tx.db.opts.label != kv.ChainDB {
		return
	}

	info, err := tx.db.env.Info(tx.tx)
	if err != nil {
		return
	}
	if info.SinceReaderCheck.Hours() > 1 {
		if staleReaders, err := tx.db.env.ReaderCheck(); err != nil {
			tx.db.log.Error("failed ReaderCheck", "err", err)
		} else if staleReaders > 0 {
			tx.db.log.Info("cleared reader slots from dead processes", "amount", staleReaders)
		}
	}

	kv.DbSize.SetUint64(info.Geo.Current)
	kv.DbPgopsNewly.SetUint64(info.PageOps.Newly)
	kv.DbPgopsCow.SetUint64(info.PageOps.Cow)
	kv.DbPgopsClone.SetUint64(info.PageOps.Clone)
	kv.DbPgopsSplit.SetUint64(info.PageOps.Split)
	kv.DbPgopsMerge.SetUint64(info.PageOps.Merge)
	kv.DbPgopsSpill.SetUint64(info.PageOps.Spill)
	kv.DbPgopsUnspill.SetUint64(info.PageOps.Unspill)
	kv.DbPgopsWops.SetUint64(info.PageOps.Wops)

	txInfo, err := tx.tx.Info(true)
	if err != nil {
		return
	}

	kv.TxDirty.SetUint64(txInfo.SpaceDirty)
	kv.TxLimit.SetUint64(tx.db.txSize)
	kv.TxSpill.SetUint64(txInfo.Spill)
	kv.TxUnspill.SetUint64(txInfo.Unspill)

	gc, err := tx.BucketStat("gc")
	if err != nil {
		return
	}
	kv.GcLeafMetric.SetUint64(gc.LeafPages)
	kv.GcOverflowMetric.SetUint64(gc.OverflowPages)
	kv.GcPagesMetric.SetUint64((gc.LeafPages + gc.OverflowPages) * tx.db.opts.pageSize / 8)

	// Flush per-transaction I/O counters to global metrics.
	if rc := tx.readCount.Load(); rc > 0 {
		kv.DbReadCount.Add(float64(rc))
		kv.DbReadBytes.Add(float64(tx.readBytes.Load()))
	}
	if wc := tx.writeCount.Load(); wc > 0 {
		kv.DbWriteCount.Add(float64(wc))
		kv.DbWriteBytes.Add(float64(tx.writeBytes.Load()))
	}
}
