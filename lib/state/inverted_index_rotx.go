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

	"github.com/RoaringBitmap/roaring/roaring64"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/bitmapdb"
	"github.com/n42blockchain/N42/lib/kv/iter"
	"github.com/n42blockchain/N42/lib/kv/order"
	"github.com/n42blockchain/N42/lib/recsplit"
	"github.com/n42blockchain/N42/lib/recsplit/eliasfano32"
	"github.com/n42blockchain/N42/lib/seg"
)

type InvertedIndexRoTx struct {
	ii      *InvertedIndex
	files   []ctxItem // have no garbage (overlaps, etc...)
	getters []*seg.Getter
	readers []*recsplit.IndexReader
}

func (ii *InvertedIndex) BeginFilesRo() *InvertedIndexRoTx {
	var ic = InvertedIndexRoTx{
		ii:    ii,
		files: *ii.visibleFiles.Load(),
	}
	for _, item := range ic.files {
		if !item.src.frozen {
			item.src.refcount.Add(1)
		}
	}
	return &ic
}

func (iit *InvertedIndexRoTx) Close() {
	for _, item := range iit.files {
		if item.src.frozen {
			continue
		}
		refCnt := item.src.refcount.Add(-1)
		//GC: last reader responsible to remove useless files: close it and delete
		if refCnt == 0 && item.src.canDelete.Load() {
			item.src.closeFilesAndRemove()
		}
	}

	for _, r := range iit.readers {
		r.Close()
	}
}

func (iit *InvertedIndexRoTx) statelessGetter(i int) *seg.Getter {
	if iit.getters == nil {
		iit.getters = make([]*seg.Getter, len(iit.files))
	}
	r := iit.getters[i]
	if r == nil {
		r = iit.files[i].src.decompressor.MakeGetter()
		iit.getters[i] = r
	}
	return r
}

func (iit *InvertedIndexRoTx) statelessIdxReader(i int) *recsplit.IndexReader {
	if iit.readers == nil {
		iit.readers = make([]*recsplit.IndexReader, len(iit.files))
	}
	r := iit.readers[i]
	if r == nil {
		r = iit.files[i].src.index.GetReaderFromPool()
		iit.readers[i] = r
	}
	return r
}

func (iit *InvertedIndexRoTx) getFile(from, to uint64) (it ctxItem, ok bool) {
	for _, item := range iit.files {
		if item.startTxNum == from && item.endTxNum == to {
			return item, true
		}
	}
	return it, false
}

// IdxRange - return range of txNums for given `key`
// is to be used in public API, therefore it relies on read-only transaction
// so that iteration can be done even when the inverted index is being updated.
// [startTxNum; endNumTx)
func (iit *InvertedIndexRoTx) IdxRange(key []byte, startTxNum, endTxNum int, asc order.By, limit int, roTx kv.Tx) (iter.U64, error) {
	frozenIt, err := iit.iterateRangeFrozen(key, startTxNum, endTxNum, asc, limit)
	if err != nil {
		return nil, err
	}
	recentIt, err := iit.recentIterateRange(key, startTxNum, endTxNum, asc, limit, roTx)
	if err != nil {
		return nil, err
	}
	return iter.Union[uint64](frozenIt, recentIt, asc, limit), nil
}

func (iit *InvertedIndexRoTx) recentIterateRange(key []byte, startTxNum, endTxNum int, asc order.By, limit int, roTx kv.Tx) (iter.U64, error) {
	//optimization: return empty pre-allocated iterator if range is frozen
	if asc {
		isFrozenRange := len(iit.files) > 0 && endTxNum >= 0 && iit.files[len(iit.files)-1].endTxNum >= uint64(endTxNum)
		if isFrozenRange {
			return iter.EmptyU64, nil
		}
	} else {
		isFrozenRange := len(iit.files) > 0 && startTxNum >= 0 && iit.files[len(iit.files)-1].endTxNum >= uint64(startTxNum)
		if isFrozenRange {
			return iter.EmptyU64, nil
		}
	}

	var from []byte
	if startTxNum >= 0 {
		from = make([]byte, 8)
		binary.BigEndian.PutUint64(from, uint64(startTxNum))
	}

	var to []byte
	if endTxNum >= 0 {
		to = make([]byte, 8)
		binary.BigEndian.PutUint64(to, uint64(endTxNum))
	}

	it, err := roTx.RangeDupSort(iit.ii.indexTable, key, from, to, asc, limit)
	if err != nil {
		return nil, err
	}
	return iter.TransformKV2U64(it, func(_, v []byte) (uint64, error) {
		return decodeTxNumPrefix("InvertedIndexRoTx.recentIterateRange", v)
	}), nil
}

func (iit *InvertedIndexRoTx) iterateRangeFrozen(key []byte, startTxNum, endTxNum int, asc order.By, limit int) (*FrozenInvertedIdxIter, error) {
	if asc && (startTxNum >= 0 && endTxNum >= 0) && startTxNum > endTxNum {
		return nil, fmt.Errorf("startTxNum=%d epected to be lower than endTxNum=%d", startTxNum, endTxNum)
	}
	if !asc && (startTxNum >= 0 && endTxNum >= 0) && startTxNum < endTxNum {
		return nil, fmt.Errorf("startTxNum=%d epected to be bigger than endTxNum=%d", startTxNum, endTxNum)
	}

	it := &FrozenInvertedIdxIter{
		key:         key,
		startTxNum:  startTxNum,
		endTxNum:    endTxNum,
		indexTable:  iit.ii.indexTable,
		orderAscend: asc,
		limit:       limit,
		ef:          eliasfano32.NewEliasFano(1, 1),
	}
	if asc {
		for i := len(iit.files) - 1; i >= 0; i-- {
			// [from,to) && from < to
			if endTxNum >= 0 && int(iit.files[i].startTxNum) >= endTxNum {
				continue
			}
			if startTxNum >= 0 && iit.files[i].endTxNum <= uint64(startTxNum) {
				break
			}
			it.stack = append(it.stack, iit.files[i])
			it.stack[len(it.stack)-1].getter = it.stack[len(it.stack)-1].src.decompressor.MakeGetter()
			it.stack[len(it.stack)-1].reader = it.stack[len(it.stack)-1].src.index.GetReaderFromPool()
			it.hasNext = true
		}
	} else {
		for i := 0; i < len(iit.files); i++ {
			// [from,to) && from > to
			if endTxNum >= 0 && int(iit.files[i].endTxNum) <= endTxNum {
				continue
			}
			if startTxNum >= 0 && iit.files[i].startTxNum > uint64(startTxNum) {
				break
			}

			it.stack = append(it.stack, iit.files[i])
			it.stack[len(it.stack)-1].getter = it.stack[len(it.stack)-1].src.decompressor.MakeGetter()
			it.stack[len(it.stack)-1].reader = it.stack[len(it.stack)-1].src.index.GetReaderFromPool()
			it.hasNext = true
		}
	}
	it.advance()
	return it, nil
}

func (iit *InvertedIndexRoTx) IterateChangedKeys(startTxNum, endTxNum uint64, roTx kv.Tx) InvertedIterator1 {
	var ii1 InvertedIterator1
	ii1.hasNextInDb = true
	ii1.roTx = roTx
	ii1.indexTable = iit.ii.indexTable
	for _, item := range iit.files {
		if item.endTxNum <= startTxNum {
			continue
		}
		if item.startTxNum >= endTxNum {
			break
		}
		if item.endTxNum >= endTxNum {
			ii1.hasNextInDb = false
		}
		g := item.src.decompressor.MakeGetter()
		if g.HasNext() {
			key, _ := g.NextUncompressed()
			heap.Push(&ii1.h, &ReconItem{startTxNum: item.startTxNum, endTxNum: item.endTxNum, g: g, txNum: ^item.endTxNum, key: key})
			ii1.hasNextInFiles = true
		}
	}
	binary.BigEndian.PutUint64(ii1.startTxKey[:], startTxNum)
	ii1.startTxNum = startTxNum
	ii1.endTxNum = endTxNum
	ii1.advanceInDb()
	ii1.advanceInFiles()
	ii1.advance()
	return ii1
}

func (iit *InvertedIndexRoTx) staticFilesInRange(startTxNum, endTxNum uint64) ([]*filesItem, int) {
	files := make([]*filesItem, 0, len(iit.files))
	var startJ int

	for _, item := range iit.files {
		if item.startTxNum < startTxNum {
			startJ++
			continue
		}
		if item.endTxNum > endTxNum {
			break
		}
		files = append(files, item.src)
	}
	for _, f := range files {
		if f == nil {
			panic("must not happen")
		}
	}

	return files, startJ
}

func (iit *InvertedIndexRoTx) frozenTo() uint64 {
	if len(iit.files) == 0 {
		return 0
	}
	for i := len(iit.files) - 1; i >= 0; i-- {
		if iit.files[i].src.frozen {
			return iit.files[i].endTxNum
		}
	}
	return 0
}

// FrozenInvertedIdxIter allows iteration over range of tx numbers
// Iteration is not implemented via callback function, because there is often
// a requirement for iterators to be composable (for example, to implement AND and OR for indices)
// FrozenInvertedIdxIter must be closed after use to prevent leaking of resources like cursor
type FrozenInvertedIdxIter struct {
	key                  []byte
	startTxNum, endTxNum int
	limit                int
	orderAscend          order.By

	efIt       iter.Unary[uint64]
	indexTable string
	stack      []ctxItem

	nextN   uint64
	hasNext bool
	err     error

	ef *eliasfano32.EliasFano
}

func (it *FrozenInvertedIdxIter) Close() {
	for _, item := range it.stack {
		item.reader.Close()
	}
}

func (it *FrozenInvertedIdxIter) advance() {
	if it.hasNext {
		it.advanceInFiles()
	}
}

func (it *FrozenInvertedIdxIter) HasNext() bool {
	if it.err != nil { // always true, then .Next() call will return this error
		return true
	}
	if it.limit == 0 { // limit reached
		return false
	}
	return it.hasNext
}

func (it *FrozenInvertedIdxIter) Next() (uint64, error) {
	if it.err != nil {
		return 0, it.err
	}
	return it.next(), nil
}

func (it *FrozenInvertedIdxIter) next() uint64 {
	it.limit--
	n := it.nextN
	it.advance()
	return n
}

func (it *FrozenInvertedIdxIter) advanceInFiles() {
	for {
		for it.efIt == nil { //TODO: this loop may be optimized by LocalityIndex
			if len(it.stack) == 0 {
				it.hasNext = false
				return
			}
			item := it.stack[len(it.stack)-1]
			it.stack = it.stack[:len(it.stack)-1]
			offset, ok := item.reader.Lookup(it.key)
			if !ok {
				continue
			}
			g := item.getter
			g.Reset(offset)
			k, _ := g.NextUncompressed()
			if bytes.Equal(k, it.key) {
				eliasVal, _ := g.NextUncompressed()
				it.ef.Reset(eliasVal)
				if it.orderAscend {
					efiter := it.ef.Iterator()
					if it.startTxNum > 0 {
						efiter.Seek(uint64(it.startTxNum))
					}
					it.efIt = efiter
				} else {
					it.efIt = it.ef.ReverseIterator()
				}
			}
		}

		//TODO: add seek method
		//Asc:  [from, to) AND from > to
		//Desc: [from, to) AND from < to
		if it.orderAscend {
			for it.efIt.HasNext() {
				n, _ := it.efIt.Next()
				if it.endTxNum >= 0 && int(n) >= it.endTxNum {
					it.hasNext = false
					return
				}
				if int(n) >= it.startTxNum {
					it.hasNext = true
					it.nextN = n
					return
				}
			}
		} else {
			for it.efIt.HasNext() {
				n, _ := it.efIt.Next()
				if int(n) <= it.endTxNum {
					it.hasNext = false
					return
				}
				if it.startTxNum >= 0 && int(n) <= it.startTxNum {
					it.hasNext = true
					it.nextN = n
					return
				}
			}
		}
		it.efIt = nil // Exhausted this iterator
	}
}

// RecentInvertedIdxIter allows iteration over range of tx numbers
// Iteration is not implemented via callback function, because there is often
// a requirement for iterators to be composable (for example, to implement AND and OR for indices)
type RecentInvertedIdxIter struct {
	key                  []byte
	startTxNum, endTxNum int
	limit                int
	orderAscend          order.By

	roTx       kv.Tx
	cursor     kv.CursorDupSort
	indexTable string

	nextN   uint64
	hasNext bool
	err     error

	bm *roaring64.Bitmap
}

func (it *RecentInvertedIdxIter) Close() {
	if it.cursor != nil {
		it.cursor.Close()
	}
	bitmapdb.ReturnToPool64(it.bm)
}

func (it *RecentInvertedIdxIter) fail(err error) {
	it.err = err
	it.hasNext = false
}

func (it *RecentInvertedIdxIter) advanceInDB() {
	var v []byte
	var err error
	if it.cursor == nil {
		if it.cursor, err = it.roTx.CursorDupSort(it.indexTable); err != nil {
			it.fail(err)
			return
		}
		var k []byte
		if k, _, err = it.cursor.SeekExact(it.key); err != nil {
			it.fail(err)
			return
		}
		if k == nil {
			it.hasNext = false
			return
		}
		//Asc:  [from, to) AND from > to
		//Desc: [from, to) AND from < to
		var keyBytes [8]byte
		if it.startTxNum > 0 {
			binary.BigEndian.PutUint64(keyBytes[:], uint64(it.startTxNum))
		}
		if v, err = it.cursor.SeekBothRange(it.key, keyBytes[:]); err != nil {
			it.fail(err)
			return
		}
		if v == nil {
			if !it.orderAscend {
				_, v, err = it.cursor.PrevDup()
				if err != nil {
					it.fail(err)
					return
				}
			}
			if v == nil {
				it.hasNext = false
				return
			}
		}
	} else {
		if it.orderAscend {
			_, v, err = it.cursor.NextDup()
			if err != nil {
				it.fail(err)
				return
			}
		} else {
			_, v, err = it.cursor.PrevDup()
			if err != nil {
				it.fail(err)
				return
			}
		}
	}

	//Asc:  [from, to) AND from > to
	//Desc: [from, to) AND from < to
	if it.orderAscend {
		for ; v != nil; _, v, err = it.cursor.NextDup() {
			if err != nil {
				it.fail(err)
				return
			}
			n, err := decodeTxNumPrefix("RecentInvertedIdxIter.advanceInDB", v)
			if err != nil {
				it.fail(err)
				return
			}
			if it.endTxNum >= 0 && int(n) >= it.endTxNum {
				it.hasNext = false
				return
			}
			if int(n) >= it.startTxNum {
				it.hasNext = true
				it.nextN = n
				return
			}
		}
	} else {
		for ; v != nil; _, v, err = it.cursor.PrevDup() {
			if err != nil {
				it.fail(err)
				return
			}
			n, err := decodeTxNumPrefix("RecentInvertedIdxIter.advanceInDB", v)
			if err != nil {
				it.fail(err)
				return
			}
			if int(n) <= it.endTxNum {
				it.hasNext = false
				return
			}
			if it.startTxNum >= 0 && int(n) <= it.startTxNum {
				it.hasNext = true
				it.nextN = n
				return
			}
		}
	}

	it.hasNext = false
}

func (it *RecentInvertedIdxIter) advance() {
	if it.hasNext {
		it.advanceInDB()
	}
}

func (it *RecentInvertedIdxIter) HasNext() bool {
	if it.err != nil { // always true, then .Next() call will return this error
		return true
	}
	if it.limit == 0 { // limit reached
		return false
	}
	return it.hasNext
}

func (it *RecentInvertedIdxIter) Next() (uint64, error) {
	if it.err != nil {
		return 0, it.err
	}
	it.limit--
	n := it.nextN
	it.advance()
	return n, nil
}

// InvertedIterator1 iterates over changed keys in both files and DB.
type InvertedIterator1 struct {
	roTx           kv.Tx
	cursor         kv.CursorDupSort
	indexTable     string
	key            []byte
	h              ReconHeap
	nextKey        []byte
	nextFileKey    []byte
	nextDbKey      []byte
	endTxNum       uint64
	startTxNum     uint64
	startTxKey     [8]byte
	hasNextInDb    bool
	hasNextInFiles bool
	err            error
}

func (it *InvertedIterator1) Close() {
	if it.cursor != nil {
		it.cursor.Close()
	}
}

func (it *InvertedIterator1) advanceInFiles() {
	for it.h.Len() > 0 {
		top := heap.Pop(&it.h).(*ReconItem)
		key := top.key
		val, _ := top.g.NextUncompressed()
		if top.g.HasNext() {
			top.key, _ = top.g.NextUncompressed()
			heap.Push(&it.h, top)
		}
		if !bytes.Equal(key, it.key) {
			ef, _ := eliasfano32.ReadEliasFano(val)
			min := ef.Get(0)
			max := ef.Max()
			if min < it.endTxNum && max >= it.startTxNum { // Intersection of [min; max) and [it.startTxNum; it.endTxNum)
				it.key = key
				it.nextFileKey = key
				return
			}
		}
	}
	it.hasNextInFiles = false
}

func (it *InvertedIterator1) advanceInDb() {
	var k, v []byte
	var err error
	if it.cursor == nil {
		if it.cursor, err = it.roTx.CursorDupSort(it.indexTable); err != nil {
			it.err = err
			return
		}
		if k, _, err = it.cursor.First(); err != nil {
			it.err = err
			return
		}
	} else {
		if k, _, err = it.cursor.NextNoDup(); err != nil {
			it.err = err
			return
		}
	}
	for k != nil {
		if v, err = it.cursor.SeekBothRange(k, it.startTxKey[:]); err != nil {
			it.err = err
			return
		}
		if v != nil {
			txNum, err := decodeTxNumPrefix("InvertedIterator1.advanceInDb", v)
			if err != nil {
				it.err = err
				return
			}
			if txNum < it.endTxNum {
				it.nextDbKey = append(it.nextDbKey[:0], k...)
				return
			}
		}
		// SeekBothRange with no matching value leaves the DupSort cursor's inner
		// dup-cursor in an indeterminate state. Re-seek to restore a valid cursor
		// position before calling NextNoDup.
		if k, _, err = it.cursor.Seek(k); err != nil {
			it.err = err
			return
		}
		if k, _, err = it.cursor.NextNoDup(); err != nil {
			it.err = err
			return
		}
	}
	it.cursor.Close()
	it.cursor = nil
	it.hasNextInDb = false
}

func (it *InvertedIterator1) advance() {
	if it.hasNextInFiles {
		if it.hasNextInDb {
			c := bytes.Compare(it.nextFileKey, it.nextDbKey)
			if c < 0 {
				it.nextKey = append(it.nextKey[:0], it.nextFileKey...)
				it.advanceInFiles()
			} else if c > 0 {
				it.nextKey = append(it.nextKey[:0], it.nextDbKey...)
				it.advanceInDb()
			} else {
				it.nextKey = append(it.nextKey[:0], it.nextFileKey...)
				it.advanceInDb()
				it.advanceInFiles()
			}
		} else {
			it.nextKey = append(it.nextKey[:0], it.nextFileKey...)
			it.advanceInFiles()
		}
	} else if it.hasNextInDb {
		it.nextKey = append(it.nextKey[:0], it.nextDbKey...)
		it.advanceInDb()
	} else {
		it.nextKey = nil
	}
}

func (it *InvertedIterator1) HasNext() bool {
	return it.err == nil && (it.hasNextInFiles || it.hasNextInDb || it.nextKey != nil)
}

// Err returns any error encountered during iteration.
// Callers should check Err after HasNext returns false.
func (it *InvertedIterator1) Err() error {
	return it.err
}

func (it *InvertedIterator1) Next(keyBuf []byte) []byte {
	result := append(keyBuf, it.nextKey...)
	it.advance()
	return result
}
