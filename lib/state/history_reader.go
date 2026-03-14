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
	"math"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/common/cmp"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/iter"
	"github.com/n42blockchain/N42/lib/kv/order"
	"github.com/n42blockchain/N42/lib/recsplit"
	"github.com/n42blockchain/N42/lib/recsplit/eliasfano32"
	"github.com/n42blockchain/N42/lib/seg"
)

type HistoryRoTx struct {
	h   *History
	iit *InvertedIndexRoTx

	files   []ctxItem // have no garbage (canDelete=true, overlaps, etc...)
	getters []*seg.Getter
	readers []*recsplit.IndexReader

	trace bool
}

func (h *History) BeginFilesRo() *HistoryRoTx {
	var hc = HistoryRoTx{
		h:     h,
		iit:   h.InvertedIndex.BeginFilesRo(),
		files: *h.visibleFiles.Load(),

		trace: false,
	}
	for _, item := range hc.files {
		if !item.src.frozen {
			item.src.refcount.Add(1)
		}
	}

	return &hc
}

func (ht *HistoryRoTx) statelessGetter(i int) *seg.Getter {
	if ht.getters == nil {
		ht.getters = make([]*seg.Getter, len(ht.files))
	}
	r := ht.getters[i]
	if r == nil {
		r = ht.files[i].src.decompressor.MakeGetter()
		ht.getters[i] = r
	}
	return r
}

func (ht *HistoryRoTx) statelessIdxReader(i int) *recsplit.IndexReader {
	if ht.readers == nil {
		ht.readers = make([]*recsplit.IndexReader, len(ht.files))
	}
	r := ht.readers[i]
	if r == nil {
		r = ht.files[i].src.index.GetReaderFromPool()
		ht.readers[i] = r
	}
	return r
}

func (ht *HistoryRoTx) Close() {
	ht.iit.Close()
	for _, item := range ht.files {
		if item.src.frozen {
			continue
		}
		refCnt := item.src.refcount.Add(-1)
		// GC: last reader responsible to remove useless files: close it and delete
		if refCnt == 0 && item.src.canDelete.Load() {
			item.src.closeFilesAndRemove()
		}
	}
	for _, r := range ht.readers {
		r.Close()
	}

}

func (ht *HistoryRoTx) getFile(from, to uint64) (it ctxItem, ok bool) {
	for _, item := range ht.files {
		if item.startTxNum == from && item.endTxNum == to {
			return item, true
		}
	}
	return it, false
}

func (ht *HistoryRoTx) GetNoState(key []byte, txNum uint64) ([]byte, bool, error) {
	var foundTxNum uint64
	var foundEndTxNum uint64
	var foundStartTxNum uint64
	var found bool
	var findInFile = func(item ctxItem) bool {
		reader := ht.iit.statelessIdxReader(item.i)
		if reader.Empty() {
			return true
		}
		offset, ok := reader.Lookup(key)
		if !ok {
			return false
		}
		g := ht.iit.statelessGetter(item.i)
		g.Reset(offset)
		k, _ := g.NextUncompressed()

		if !bytes.Equal(k, key) {
			return true
		}
		eliasVal, _ := g.NextUncompressed()
		ef, _ := eliasfano32.ReadEliasFano(eliasVal)
		n, ok := ef.Search(txNum)
		if ht.trace {
			n2, _ := ef.Search(n + 1)
			n3, _ := ef.Search(n - 1)
			fmt.Printf("hist: files: %s %d<-%d->%d->%d, %x\n", ht.h.filenameBase, n3, txNum, n, n2, key)
		}
		if ok {
			foundTxNum = n
			foundEndTxNum = item.endTxNum
			foundStartTxNum = item.startTxNum
			found = true
			return false
		}
		return true
	}

	for _, item := range ht.iit.files {
		if !findInFile(item) {
			break
		}
	}

	if found {
		historyItem, ok := ht.getFile(foundStartTxNum, foundEndTxNum)
		if !ok {
			return nil, false, fmt.Errorf("hist file not found: key=%x, %s.%d-%d", key, ht.h.filenameBase, foundStartTxNum/ht.h.aggregationStep, foundEndTxNum/ht.h.aggregationStep)
		}
		var txKey [8]byte
		binary.BigEndian.PutUint64(txKey[:], foundTxNum)
		reader := ht.statelessIdxReader(historyItem.i)
		offset, ok := reader.Lookup2(txKey[:], key)
		if !ok {
			return nil, false, nil
		}
		g := ht.statelessGetter(historyItem.i)
		g.Reset(offset)
		if ht.h.compressVals {
			v, _ := g.Next(nil)
			return v, true, nil
		}
		v, _ := g.NextUncompressed()
		return v, true, nil
	}
	return nil, false, nil
}

// GetNoStateWithRecent searches history for a value of specified key before txNum
// second return value is true if the value is found in the history (even if it is nil)
func (ht *HistoryRoTx) GetNoStateWithRecent(key []byte, txNum uint64, roTx kv.Tx) ([]byte, bool, error) {
	v, ok, err := ht.GetNoState(key, txNum)
	if err != nil {
		return nil, ok, err
	}
	if ok {
		return v, true, nil
	}

	// Value not found in history files, look in the recent history
	if roTx == nil {
		return nil, false, fmt.Errorf("roTx is nil")
	}
	return ht.getNoStateFromDB(key, txNum, roTx)
}

func (ht *HistoryRoTx) getNoStateFromDB(key []byte, txNum uint64, tx kv.Tx) ([]byte, bool, error) {
	if ht.h.largeValues {
		c, err := tx.Cursor(ht.h.historyValsTable)
		if err != nil {
			return nil, false, err
		}
		defer c.Close()
		seek := make([]byte, len(key)+8)
		copy(seek, key)
		binary.BigEndian.PutUint64(seek[len(key):], txNum)
		kAndTxNum, val, err := c.Seek(seek)
		if err != nil {
			return nil, false, err
		}
		if kAndTxNum == nil || !bytes.Equal(kAndTxNum[:len(kAndTxNum)-8], key) {
			return nil, false, nil
		}
		// val == []byte{} means key was created in this txNum and doesn't exist before.
		return val, true, nil
	}
	c, err := tx.CursorDupSort(ht.h.historyValsTable)
	if err != nil {
		return nil, false, err
	}
	defer c.Close()
	seek := make([]byte, len(key)+8)
	copy(seek, key)
	binary.BigEndian.PutUint64(seek[len(key):], txNum)
	val, err := c.SeekBothRange(key, seek[len(key):])
	if err != nil {
		return nil, false, err
	}
	if val == nil {
		return nil, false, nil
	}
	// `val == []byte{}` means key was created in this txNum and doesn't exist before.
	return val[8:], true, nil
}

func (ht *HistoryRoTx) WalkAsOf(startTxNum uint64, from, to []byte, roTx kv.Tx, limit int) iter.KV {
	hi := &StateAsOfIterF{
		from: from, to: to, limit: limit,

		hc:           ht,
		compressVals: ht.h.compressVals,
		startTxNum:   startTxNum,
	}
	for _, item := range ht.iit.files {
		if item.endTxNum <= startTxNum {
			continue
		}
		// TODO: seek(from)
		g := item.src.decompressor.MakeGetter()
		g.Reset(0)
		if g.HasNext() {
			key, offset := g.NextUncompressed()
			heap.Push(&hi.h, &ReconItem{g: g, key: key, startTxNum: item.startTxNum, endTxNum: item.endTxNum, txNum: item.endTxNum, startOffset: offset, lastOffset: offset})
		}
	}
	binary.BigEndian.PutUint64(hi.startTxKey[:], startTxNum)
	if err := hi.advanceInFiles(); err != nil {
		return &errKVIter{err: err}
	}

	dbit := &StateAsOfIterDB{
		largeValues: ht.h.largeValues,
		roTx:        roTx,
		valsTable:   ht.h.historyValsTable,
		from:        from, to: to, limit: limit,

		startTxNum: startTxNum,
	}
	binary.BigEndian.PutUint64(dbit.startTxKey[:], startTxNum)
	if err := dbit.advance(); err != nil {
		return &errKVIter{err: err}
	}
	return iter.UnionKV(hi, dbit, limit)
}

type errKVIter struct {
	err error
}

func (it *errKVIter) HasNext() bool {
	return it.err != nil
}

func (it *errKVIter) Next() ([]byte, []byte, error) {
	err := it.err
	it.err = nil
	return nil, nil, err
}

func (ht *HistoryRoTx) iterateChangedFrozen(fromTxNum, toTxNum int, asc order.By, limit int) (iter.KV, error) {
	if !asc {
		return nil, fmt.Errorf("history iteration in descending order is not supported")
	}
	if len(ht.iit.files) == 0 {
		return iter.EmptyKV, nil
	}

	if fromTxNum >= 0 && ht.iit.files[len(ht.iit.files)-1].endTxNum <= uint64(fromTxNum) {
		return iter.EmptyKV, nil
	}

	hi := &HistoryChangesIterFiles{
		hc:           ht,
		compressVals: ht.h.compressVals,
		startTxNum:   cmp.Max(0, uint64(fromTxNum)),
		endTxNum:     toTxNum,
		limit:        limit,
	}
	if fromTxNum >= 0 {
		binary.BigEndian.PutUint64(hi.startTxKey[:], uint64(fromTxNum))
	}
	for _, item := range ht.iit.files {
		if fromTxNum >= 0 && item.endTxNum <= uint64(fromTxNum) {
			continue
		}
		if toTxNum >= 0 && item.startTxNum >= uint64(toTxNum) {
			break
		}
		g := item.src.decompressor.MakeGetter()
		g.Reset(0)
		if g.HasNext() {
			key, offset := g.NextUncompressed()
			heap.Push(&hi.h, &ReconItem{g: g, key: key, startTxNum: item.startTxNum, endTxNum: item.endTxNum, txNum: item.endTxNum, startOffset: offset, lastOffset: offset})
		}
	}
	if err := hi.advance(); err != nil {
		return nil, err
	}
	return hi, nil
}

func (ht *HistoryRoTx) iterateChangedRecent(fromTxNum, toTxNum int, asc order.By, limit int, roTx kv.Tx) (iter.KV, error) {
	if asc == order.Desc {
		return nil, fmt.Errorf("history iteration in descending order is not supported")
	}
	rangeIsInFiles := toTxNum >= 0 && len(ht.iit.files) > 0 && ht.iit.files[len(ht.iit.files)-1].endTxNum >= uint64(toTxNum)
	if rangeIsInFiles {
		return iter.EmptyKV, nil
	}
	dbi := &HistoryChangesIterDB{
		endTxNum:    toTxNum,
		roTx:        roTx,
		largeValues: ht.h.largeValues,
		valsTable:   ht.h.historyValsTable,
		limit:       limit,
	}
	if fromTxNum >= 0 {
		binary.BigEndian.PutUint64(dbi.startTxKey[:], uint64(fromTxNum))
	}
	if err := dbi.advance(); err != nil {
		return nil, err
	}
	return dbi, nil
}

func (ht *HistoryRoTx) HistoryRange(fromTxNum, toTxNum int, asc order.By, limit int, roTx kv.Tx) (iter.KV, error) {
	if asc == order.Desc {
		return nil, fmt.Errorf("history iteration in descending order is not supported")
	}
	itOnFiles, err := ht.iterateChangedFrozen(fromTxNum, toTxNum, asc, limit)
	if err != nil {
		return nil, err
	}
	itOnDB, err := ht.iterateChangedRecent(fromTxNum, toTxNum, asc, limit, roTx)
	if err != nil {
		return nil, err
	}

	return iter.UnionKV(itOnFiles, itOnDB, limit), nil
}

func (ht *HistoryRoTx) idxRangeRecent(key []byte, startTxNum, endTxNum int, asc order.By, limit int, roTx kv.Tx) (iter.U64, error) {
	var dbIt iter.U64
	if ht.h.largeValues {
		if asc {
			from := make([]byte, len(key)+8)
			copy(from, key)
			var fromTxNum uint64
			if startTxNum >= 0 {
				fromTxNum = uint64(startTxNum)
			}
			binary.BigEndian.PutUint64(from[len(key):], fromTxNum)

			to := common.Copy(from)
			toTxNum := uint64(math.MaxUint64)
			if endTxNum >= 0 {
				toTxNum = uint64(endTxNum)
			}
			binary.BigEndian.PutUint64(to[len(key):], toTxNum)

			it, err := roTx.RangeAscend(ht.h.historyValsTable, from, to, limit)
			if err != nil {
				return nil, err
			}
			dbIt = iter.TransformKV2U64(it, func(k, _ []byte) (uint64, error) {
				return binary.BigEndian.Uint64(k[len(k)-8:]), nil
			})
		} else {
			return nil, fmt.Errorf("index range in descending order is not supported for large values")
		}
	} else {
		if asc {
			var from, to []byte
			if startTxNum >= 0 {
				from = make([]byte, 8)
				binary.BigEndian.PutUint64(from, uint64(startTxNum))
			}
			if endTxNum >= 0 {
				to = make([]byte, 8)
				binary.BigEndian.PutUint64(to, uint64(endTxNum))
			}
			it, err := roTx.RangeDupSort(ht.h.historyValsTable, key, from, to, asc, limit)
			if err != nil {
				return nil, err
			}
			dbIt = iter.TransformKV2U64(it, func(_, v []byte) (uint64, error) {
				return binary.BigEndian.Uint64(v), nil
			})
		} else {
			return nil, fmt.Errorf("index range in descending order is not supported")
		}
	}

	return dbIt, nil
}

func (ht *HistoryRoTx) IdxRange(key []byte, startTxNum, endTxNum int, asc order.By, limit int, roTx kv.Tx) (iter.U64, error) {
	frozenIt, err := ht.iit.iterateRangeFrozen(key, startTxNum, endTxNum, asc, limit)
	if err != nil {
		return nil, err
	}
	recentIt, err := ht.idxRangeRecent(key, startTxNum, endTxNum, asc, limit, roTx)
	if err != nil {
		return nil, err
	}
	return iter.Union[uint64](frozenIt, recentIt, asc, limit), nil
}
