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
	"github.com/n42blockchain/N42/lib/recsplit/eliasfano32"
)

type HistoryChangesIterFiles struct {
	hc           *HistoryRoTx
	nextVal      []byte
	nextKey      []byte
	h            ReconHeap
	startTxNum   uint64
	endTxNum     int
	startTxKey   [8]byte
	txnKey       [8]byte
	compressVals bool

	k, v, kBackup, vBackup []byte
	err                    error
	limit                  int
}

func (hi *HistoryChangesIterFiles) Close() {
}

func (hi *HistoryChangesIterFiles) advance() error {
	for hi.h.Len() > 0 {
		top := heap.Pop(&hi.h).(*ReconItem)
		key := top.key
		var idxVal []byte
		if hi.compressVals {
			idxVal, _ = top.g.Next(nil)
		} else {
			idxVal, _ = top.g.NextUncompressed()
		}
		if top.g.HasNext() {
			if hi.compressVals {
				top.key, _ = top.g.Next(nil)
			} else {
				top.key, _ = top.g.NextUncompressed()
			}
			heap.Push(&hi.h, top)
		}

		if bytes.Equal(key, hi.nextKey) {
			continue
		}
		ef, _ := eliasfano32.ReadEliasFano(idxVal)
		n, ok := ef.Search(hi.startTxNum) //TODO: if startTxNum==0, can do ef.Get(0)
		if !ok {
			continue
		}
		if int(n) >= hi.endTxNum {
			continue
		}

		hi.nextKey = key
		binary.BigEndian.PutUint64(hi.txnKey[:], n)
		historyItem, ok := hi.hc.getFile(top.startTxNum, top.endTxNum)
		if !ok {
			return fmt.Errorf("HistoryChangesIterFiles: no %s file found for [%x]", hi.hc.h.filenameBase, hi.nextKey)
		}
		reader := hi.hc.statelessIdxReader(historyItem.i)
		offset, ok := reader.Lookup2(hi.txnKey[:], hi.nextKey)
		if !ok {
			continue
		}
		g := hi.hc.statelessGetter(historyItem.i)
		g.Reset(offset)
		if hi.compressVals {
			hi.nextVal, _ = g.Next(nil)
		} else {
			hi.nextVal, _ = g.NextUncompressed()
		}
		return nil
	}
	hi.nextKey = nil
	return nil
}

func (hi *HistoryChangesIterFiles) HasNext() bool {
	if hi.err != nil { // always true, then .Next() call will return this error
		return true
	}
	if hi.limit == 0 { // limit reached
		return false
	}
	if hi.nextKey == nil { // EndOfTable
		return false
	}
	return true
}

func (hi *HistoryChangesIterFiles) Next() ([]byte, []byte, error) {
	if hi.err != nil {
		return nil, nil, hi.err
	}
	hi.limit--
	hi.k, hi.v = append(hi.k[:0], hi.nextKey...), append(hi.v[:0], hi.nextVal...)

	// Satisfy iter.Dual Invariant 2
	hi.k, hi.kBackup, hi.v, hi.vBackup = hi.kBackup, hi.k, hi.vBackup, hi.v
	if err := hi.advance(); err != nil {
		hi.err = err
	}
	return hi.kBackup, hi.vBackup, nil
}

type HistoryChangesIterDB struct {
	largeValues     bool
	roTx            kv.Tx
	valsC           kv.Cursor
	valsCDup        kv.CursorDupSort
	valsTable       string
	limit, endTxNum int
	startTxKey      [8]byte

	nextKey, nextVal []byte
	k, v             []byte
	err              error
}

func (hi *HistoryChangesIterDB) Close() {
	if hi.valsC != nil {
		hi.valsC.Close()
	}
	if hi.valsCDup != nil {
		hi.valsCDup.Close()
	}
}

func (hi *HistoryChangesIterDB) advance() (err error) {
	// not large:
	//   keys: txNum -> key1+key2
	//   vals: key1+key2 -> txNum + value (DupSort)
	// large:
	//   keys: txNum -> key1+key2
	//   vals: key1+key2+txNum -> value (not DupSort)
	if hi.largeValues {
		return hi.advanceLargeVals()
	}
	return hi.advanceSmallVals()
}

func (hi *HistoryChangesIterDB) advanceLargeVals() error {
	var seek []byte
	var err error
	if hi.valsC == nil {
		if hi.valsC, err = hi.roTx.Cursor(hi.valsTable); err != nil {
			return err
		}
		firstKey, _, err := hi.valsC.First()
		if err != nil {
			return err
		}
		if firstKey == nil {
			hi.nextKey = nil
			return nil
		}
		firstKeyPrefix, err := trimTxNumSuffix("HistoryChangesIterDB.advanceLargeVals", firstKey)
		if err != nil {
			return err
		}
		seek = append(common.Copy(firstKeyPrefix), hi.startTxKey[:]...)
	} else {
		next, ok := kv.NextSubtree(hi.nextKey)
		if !ok {
			hi.nextKey = nil
			return nil
		}

		seek = append(next, hi.startTxKey[:]...)
	}
	for k, v, err := hi.valsC.Seek(seek); k != nil; k, v, err = hi.valsC.Seek(seek) {
		if err != nil {
			return err
		}
		keyPrefix, txNum, err := splitTxNumSuffix("HistoryChangesIterDB.advanceLargeVals", k)
		if err != nil {
			return err
		}
		if hi.endTxNum >= 0 && int(txNum) >= hi.endTxNum {
			next, ok := kv.NextSubtree(keyPrefix)
			if !ok {
				hi.nextKey = nil
				return nil
			}
			seek = append(next, hi.startTxKey[:]...)
			continue
		}
		seekPrefix, err := trimTxNumSuffix("HistoryChangesIterDB.advanceLargeVals", seek)
		if err != nil {
			return err
		}
		if !bytes.Equal(seekPrefix, keyPrefix) {
			seek = append(common.Copy(keyPrefix), hi.startTxKey[:]...)
			continue
		}
		hi.nextKey = keyPrefix
		hi.nextVal = v
		return nil
	}
	hi.nextKey = nil
	return nil
}

func (hi *HistoryChangesIterDB) advanceSmallVals() (err error) {
	var k []byte
	if hi.valsCDup == nil {
		if hi.valsCDup, err = hi.roTx.CursorDupSort(hi.valsTable); err != nil {
			return err
		}

		if k, _, err = hi.valsCDup.First(); err != nil {
			return err
		}
	} else {
		if k, _, err = hi.valsCDup.NextNoDup(); err != nil {
			return err
		}
	}
	for k != nil {
		v, err := hi.valsCDup.SeekBothRange(k, hi.startTxKey[:])
		if err != nil {
			return err
		}
		if v == nil {
			// SeekBothRange with no matching value leaves the DupSort cursor's
			// inner dup-cursor in an indeterminate state. Re-seek to the current
			// key to restore a valid cursor position before calling NextNoDup.
			if k, _, err = hi.valsCDup.Seek(k); err != nil {
				return err
			}
			k, _, err = hi.valsCDup.NextNoDup()
			if err != nil {
				return err
			}
			continue
		}
		if len(v) < 8 {
			return fmt.Errorf("HistoryChangesIterDB.advanceSmallVals: expected txnum prefix with at least 8 bytes, got %d", len(v))
		}
		foundTxNumVal := v[:8]
		if hi.endTxNum >= 0 && int(binary.BigEndian.Uint64(foundTxNumVal)) >= hi.endTxNum {
			k, _, err = hi.valsCDup.NextNoDup()
			if err != nil {
				return err
			}
			continue
		}
		hi.nextKey = k
		hi.nextVal = v[8:]
		return nil
	}
	hi.nextKey = nil
	return nil
}

func (hi *HistoryChangesIterDB) HasNext() bool {
	if hi.err != nil { // always true, then .Next() call will return this error
		return true
	}
	if hi.limit == 0 { // limit reached
		return false
	}
	if hi.nextKey == nil { // EndOfTable
		return false
	}
	return true
}

func (hi *HistoryChangesIterDB) Next() ([]byte, []byte, error) {
	if hi.err != nil {
		return nil, nil, hi.err
	}
	hi.limit--
	hi.k, hi.v = hi.nextKey, hi.nextVal
	if err := hi.advance(); err != nil {
		hi.err = err
	}
	return hi.k, hi.v, nil
}
