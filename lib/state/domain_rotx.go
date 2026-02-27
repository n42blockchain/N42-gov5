/*
   Copyright 2022 Erigon contributors

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

package state

import (
	"bytes"
	"container/heap"
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/seg"
)

// CursorType identifies the source of a CursorItem: file or database.
type CursorType uint8

const (
	FILE_CURSOR CursorType = iota
	DB_CURSOR
)

// CursorItem is the item in the priority queue used to do merge iteration
// over storage of a given account
type CursorItem struct {
	c        kv.CursorDupSort
	dg       *seg.Getter
	dg2      *seg.Getter
	key      []byte
	val      []byte
	endTxNum uint64
	t        CursorType // Whether this item represents state file or DB record, or tree
	reverse  bool
}

type CursorHeap []*CursorItem

func (ch CursorHeap) Len() int {
	return len(ch)
}

func (ch CursorHeap) Less(i, j int) bool {
	cmp := bytes.Compare(ch[i].key, ch[j].key)
	if cmp == 0 {
		// when keys match, the items with later blocks are preferred
		if ch[i].reverse {
			return ch[i].endTxNum > ch[j].endTxNum
		}
		return ch[i].endTxNum < ch[j].endTxNum
	}
	return cmp < 0
}

func (ch *CursorHeap) Swap(i, j int) {
	(*ch)[i], (*ch)[j] = (*ch)[j], (*ch)[i]
}

func (ch *CursorHeap) Push(x interface{}) {
	*ch = append(*ch, x.(*CursorItem))
}

func (ch *CursorHeap) Pop() interface{} {
	old := *ch
	n := len(old)
	x := old[n-1]
	old[n-1] = nil
	*ch = old[0 : n-1]
	return x
}

// DomainRoTx allows accessing the same domain from multiple go-routines
type DomainRoTx struct {
	d       *Domain
	files   []ctxItem
	getters []*seg.Getter
	readers []*BtIndex
	ht      *HistoryRoTx
	keyBuf  [60]byte // 52b key and 8b for inverted step
	numBuf  [8]byte
}

func (d *Domain) BeginFilesRo() *DomainRoTx {
	dc := &DomainRoTx{
		d:     d,
		ht:    d.History.BeginFilesRo(),
		files: *d.visibleFiles.Load(),
	}
	for _, item := range dc.files {
		if !item.src.frozen {
			item.src.refcount.Add(1)
		}
	}

	return dc
}

func (dt *DomainRoTx) Close() {
	for _, item := range dt.files {
		if item.src.frozen {
			continue
		}
		refCnt := item.src.refcount.Add(-1)
		//GC: last reader responsible to remove useless files: close it and delete
		if refCnt == 0 && item.src.canDelete.Load() {
			item.src.closeFilesAndRemove()
		}
	}
	dt.ht.Close()
}

func (dt *DomainRoTx) statelessGetter(i int) *seg.Getter {
	if dt.getters == nil {
		dt.getters = make([]*seg.Getter, len(dt.files))
	}
	r := dt.getters[i]
	if r == nil {
		r = dt.files[i].src.decompressor.MakeGetter()
		dt.getters[i] = r
	}
	return r
}

func (dt *DomainRoTx) statelessBtree(i int) *BtIndex {
	if dt.readers == nil {
		dt.readers = make([]*BtIndex, len(dt.files))
	}
	r := dt.readers[i]
	if r == nil {
		r = dt.files[i].src.bindex
		dt.readers[i] = r
	}
	return r
}

func (dt *DomainRoTx) get(key []byte, fromTxNum uint64, roTx kv.Tx) ([]byte, bool, error) {
	dt.d.stats.TotalQueries.Add(1)

	invertedStep := dt.numBuf
	binary.BigEndian.PutUint64(invertedStep[:], ^(fromTxNum / dt.d.aggregationStep))
	keyCursor, err := roTx.CursorDupSort(dt.d.keysTable)
	if err != nil {
		return nil, false, err
	}
	defer keyCursor.Close()
	foundInvStep, err := keyCursor.SeekBothRange(key, invertedStep[:])
	if err != nil {
		return nil, false, err
	}
	if len(foundInvStep) == 0 {
		dt.d.stats.HistoryQueries.Add(1)
		return dt.readFromFiles(key, fromTxNum)
	}
	copy(dt.keyBuf[:], key)
	copy(dt.keyBuf[len(key):], foundInvStep)
	v, err := roTx.GetOne(dt.d.valsTable, dt.keyBuf[:len(key)+8])
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

func (dt *DomainRoTx) Get(key1, key2 []byte, roTx kv.Tx) ([]byte, error) {
	copy(dt.keyBuf[:], key1)
	copy(dt.keyBuf[len(key1):], key2)
	// keys larger than 52 bytes will panic
	v, _, err := dt.get(dt.keyBuf[:len(key1)+len(key2)], dt.d.txNum, roTx)
	return v, err
}

var COMPARE_INDEXES = false // if true, will compare values from Btree and InvertedIndex

func (dt *DomainRoTx) readFromFiles(filekey []byte, fromTxNum uint64) ([]byte, bool, error) {
	var val []byte
	var found bool

	for i := len(dt.files) - 1; i >= 0; i-- {
		if dt.files[i].endTxNum < fromTxNum {
			break
		}
		reader := dt.statelessBtree(i)
		if reader.Empty() {
			continue
		}
		cur, err := reader.Seek(filekey)
		if err != nil {
			return nil, false, err
		}
		if cur == nil {
			continue
		}

		if bytes.Equal(cur.Key(), filekey) {
			val = cur.Value()
			found = true
			break
		}
	}
	return val, found, nil
}

// historyBeforeTxNum searches history for a value of specified key before txNum
// second return value is true if the value is found in the history (even if it is nil)
func (dt *DomainRoTx) historyBeforeTxNum(key []byte, txNum uint64, roTx kv.Tx) ([]byte, bool, error) {
	dt.d.stats.HistoryQueries.Add(1)

	v, found, err := dt.ht.GetNoState(key, txNum)
	if err != nil {
		return nil, false, err
	}
	if found {
		return v, true, nil
	}

	var anyItem bool
	var topState ctxItem
	for _, item := range dt.ht.iit.files {
		if item.endTxNum < txNum {
			continue
		}
		anyItem = true
		topState = item
		break
	}
	if anyItem {
		// If there were no changes but there were history files, the value can be obtained from value files
		var val []byte
		for i := len(dt.files) - 1; i >= 0; i-- {
			if dt.files[i].startTxNum > topState.startTxNum {
				continue
			}
			reader := dt.statelessBtree(i)
			if reader.Empty() {
				continue
			}
			cur, err := reader.Seek(key)
			if err != nil {
				dt.d.logger.Warn("failed to read history before from file", "key", key, "err", err)
				return nil, false, err
			}
			if cur == nil {
				continue
			}
			if bytes.Equal(cur.Key(), key) {
				val = cur.Value()
				break
			}
		}
		return val, true, nil
	}
	// Value not found in history files, look in the recent history
	if roTx == nil {
		return nil, false, fmt.Errorf("roTx is nil")
	}
	return dt.ht.getNoStateFromDB(key, txNum, roTx)
}

// GetBeforeTxNum does not always require usage of roTx. If it is possible to determine
// historical value based only on static files, roTx will not be used.
func (dt *DomainRoTx) GetBeforeTxNum(key []byte, txNum uint64, roTx kv.Tx) ([]byte, error) {
	v, hOk, err := dt.historyBeforeTxNum(key, txNum, roTx)
	if err != nil {
		return nil, err
	}
	if hOk {
		// if history returned marker of key creation
		// domain must return nil
		if len(v) == 0 {
			return nil, nil
		}
		return v, nil
	}
	if v, _, err = dt.get(key, txNum-1, roTx); err != nil {
		return nil, err
	}
	return v, nil
}

// IteratePrefix iterates over key-value pairs of the domain that start with given prefix
// Such iteration is not intended to be used in public API, therefore it uses read-write transaction
// inside the domain. Another version of this for public API use needs to be created, that uses
// roTx instead and supports ending the iterations before it reaches the end.
func (dt *DomainRoTx) IteratePrefix(prefix []byte, it func(k, v []byte)) error {
	dt.d.stats.HistoryQueries.Add(1)

	var cp CursorHeap
	heap.Init(&cp)
	var k, v []byte
	var err error
	keysCursor, err := dt.d.tx.CursorDupSort(dt.d.keysTable)
	if err != nil {
		return err
	}
	defer keysCursor.Close()
	if k, v, err = keysCursor.Seek(prefix); err != nil {
		return err
	}
	if bytes.HasPrefix(k, prefix) {
		keySuffix := make([]byte, len(k)+8)
		copy(keySuffix, k)
		copy(keySuffix[len(k):], v)
		step := ^binary.BigEndian.Uint64(v)
		txNum := step * dt.d.aggregationStep
		if v, err = dt.d.tx.GetOne(dt.d.valsTable, keySuffix); err != nil {
			return err
		}
		heap.Push(&cp, &CursorItem{t: DB_CURSOR, key: common.Copy(k), val: common.Copy(v), c: keysCursor, endTxNum: txNum, reverse: true})
	}

	for i, item := range dt.files {
		bg := dt.statelessBtree(i)
		if bg.Empty() {
			continue
		}

		cursor, err := bg.Seek(prefix)
		if err != nil {
			continue
		}

		g := dt.statelessGetter(i)
		key := cursor.Key()
		if bytes.HasPrefix(key, prefix) {
			val := cursor.Value()
			heap.Push(&cp, &CursorItem{t: FILE_CURSOR, key: key, val: val, dg: g, endTxNum: item.endTxNum, reverse: true})
		}
	}
	for cp.Len() > 0 {
		lastKey := common.Copy(cp[0].key)
		lastVal := common.Copy(cp[0].val)
		// Advance all the items that have this key (including the top)
		for cp.Len() > 0 && bytes.Equal(cp[0].key, lastKey) {
			ci1 := cp[0]
			switch ci1.t {
			case FILE_CURSOR:
				if ci1.dg.HasNext() {
					ci1.key, _ = ci1.dg.Next(ci1.key[:0])
					if bytes.HasPrefix(ci1.key, prefix) {
						ci1.val, _ = ci1.dg.Next(ci1.val[:0])
						heap.Fix(&cp, 0)
					} else {
						heap.Pop(&cp)
					}
				} else {
					heap.Pop(&cp)
				}
			case DB_CURSOR:
				k, v, err = ci1.c.NextNoDup()
				if err != nil {
					return err
				}
				if k != nil && bytes.HasPrefix(k, prefix) {
					ci1.key = common.Copy(k)
					keySuffix := make([]byte, len(k)+8)
					copy(keySuffix, k)
					copy(keySuffix[len(k):], v)
					if v, err = dt.d.tx.GetOne(dt.d.valsTable, keySuffix); err != nil {
						return err
					}
					ci1.val = common.Copy(v)
					heap.Fix(&cp, 0)
				} else {
					heap.Pop(&cp)
				}
			}
		}
		if len(lastVal) > 0 {
			it(lastKey, lastVal)
		}
	}
	return nil
}
