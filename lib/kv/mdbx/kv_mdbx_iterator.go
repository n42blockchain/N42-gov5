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
	"context"
	"fmt"
	"sort"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/iter"
	"github.com/n42blockchain/N42/lib/kv/order"
)

// validateRangeOrder checks that fromPrefix and toPrefix are in the correct order for the given direction.
func validateRangeOrder(fromPrefix, toPrefix []byte, asc bool) error {
	if fromPrefix == nil || toPrefix == nil {
		return nil
	}
	cmp := bytes.Compare(fromPrefix, toPrefix)
	if asc && cmp >= 0 {
		return fmt.Errorf("tx.Dual: %x must be lexicographicaly before %x", fromPrefix, toPrefix)
	}
	if !asc && cmp <= 0 {
		return fmt.Errorf("tx.Dual: %x must be lexicographicaly before %x", toPrefix, fromPrefix)
	}
	return nil
}

type cursor2iter struct {
	c  kv.Cursor
	id int
	tx *MdbxTx

	fromPrefix, toPrefix, nextK, nextV []byte
	orderAscend                        order.By
	limit                              int64
	ctx                                context.Context
}

// registerStream assigns a stream ID and registers the closer for auto-cleanup.
func (tx *MdbxTx) registerStream(s kv.Closer) int {
	id := tx.streamID
	tx.streamID++
	if tx.streams == nil {
		tx.streams = map[int]kv.Closer{}
	}
	tx.streams[id] = s
	return id
}

func (tx *MdbxTx) rangeOrderLimit(table string, fromPrefix, toPrefix []byte, orderAscend order.By, limit int) (*cursor2iter, error) {
	s := &cursor2iter{ctx: tx.ctx, tx: tx, fromPrefix: fromPrefix, toPrefix: toPrefix, orderAscend: orderAscend, limit: int64(limit)}
	s.id = tx.registerStream(s)
	return s.init(table, tx)
}
func (s *cursor2iter) init(table string, tx kv.Tx) (*cursor2iter, error) {
	if err := validateRangeOrder(s.fromPrefix, s.toPrefix, bool(s.orderAscend)); err != nil {
		return s, err
	}
	c, err := tx.Cursor(table)
	if err != nil {
		return s, err
	}
	s.c = c

	if s.fromPrefix == nil { // no initial position
		if s.orderAscend {
			s.nextK, s.nextV, err = s.c.First()
		} else {
			s.nextK, s.nextV, err = s.c.Last()
		}
		return s, err
	}

	if s.orderAscend {
		s.nextK, s.nextV, err = s.c.Seek(s.fromPrefix)
		return s, err
	} else {
		// seek exactly to given key or previous one
		s.nextK, s.nextV, err = s.c.SeekExact(s.fromPrefix)
		if err != nil {
			return s, err
		}
		if s.nextK != nil { // go to last value of this key
			if casted, ok := s.c.(kv.CursorDupSort); ok {
				s.nextV, err = casted.LastDup()
			}
		} else { // key not found, go to prev one
			s.nextK, s.nextV, err = s.c.Prev()
		}
		return s, err
	}
}

func (s *cursor2iter) advance() (err error) {
	if s.orderAscend {
		s.nextK, s.nextV, err = s.c.Next()
	} else {
		s.nextK, s.nextV, err = s.c.Prev()
	}
	return err
}

func (s *cursor2iter) Close() {
	if s.c != nil {
		s.c.Close()
		delete(s.tx.streams, s.id)
		s.c = nil
	}
}

func (s *cursor2iter) HasNext() bool {
	if s.limit == 0 { // limit reached
		return false
	}
	if s.nextK == nil { // EndOfTable
		return false
	}
	if s.toPrefix == nil { // s.nextK == nil check is above
		return true
	}

	//Asc:  [from, to) AND from > to
	//Desc: [from, to) AND from < to
	cmp := bytes.Compare(s.nextK, s.toPrefix)
	return (bool(s.orderAscend) && cmp < 0) || (!bool(s.orderAscend) && cmp > 0)
}

func (s *cursor2iter) Next() (k, v []byte, err error) {
	select {
	case <-s.ctx.Done():
		return nil, nil, s.ctx.Err()
	default:
	}
	s.limit--
	k, v = s.nextK, s.nextV
	if err = s.advance(); err != nil {
		return nil, nil, err
	}
	return k, v, nil
}

type cursorDup2iter struct {
	c  kv.CursorDupSort
	id int
	tx *MdbxTx

	key                         []byte
	fromPrefix, toPrefix, nextV []byte
	orderAscend                 bool
	limit                       int64
	ctx                         context.Context
}

func (s *cursorDup2iter) init(table string, tx kv.Tx) (*cursorDup2iter, error) {
	if err := validateRangeOrder(s.fromPrefix, s.toPrefix, s.orderAscend); err != nil {
		return s, err
	}
	c, err := tx.CursorDupSort(table)
	if err != nil {
		return s, err
	}
	s.c = c
	k, _, err := c.SeekExact(s.key)
	if err != nil {
		return s, err
	}
	if k == nil {
		return s, nil
	}

	if s.fromPrefix == nil { // no initial position
		if s.orderAscend {
			s.nextV, err = s.c.FirstDup()
		} else {
			s.nextV, err = s.c.LastDup()
		}
		return s, err
	}

	if s.orderAscend {
		s.nextV, err = s.c.SeekBothRange(s.key, s.fromPrefix)
		return s, err
	} else {
		// Seek to the first dup value >= fromPrefix, then step back if needed.
		// SeekBothExact + PrevDup would leave the cursor in invalid dup state on
		// a miss; SeekBothRange always returns a valid cursor position on success.
		s.nextV, err = s.c.SeekBothRange(s.key, s.fromPrefix)
		if err != nil {
			return s, err
		}
		if s.nextV == nil {
			// No dup value >= fromPrefix; restore cursor and use the last dup.
			if _, _, err = s.c.SeekExact(s.key); err != nil {
				return s, err
			}
			s.nextV, err = s.c.LastDup()
		} else if !bytes.Equal(s.nextV, s.fromPrefix) {
			// Found a dup value > fromPrefix; step back to the largest value < fromPrefix.
			_, s.nextV, err = s.c.PrevDup()
		}
		return s, err
	}
}

func (s *cursorDup2iter) advance() (err error) {
	if s.orderAscend {
		_, s.nextV, err = s.c.NextDup()
	} else {
		_, s.nextV, err = s.c.PrevDup()
	}
	return err
}

func (s *cursorDup2iter) Close() {
	if s.c != nil {
		s.c.Close()
		delete(s.tx.streams, s.id)
		s.c = nil
	}
}
func (s *cursorDup2iter) HasNext() bool {
	if s.limit == 0 { // limit reached
		return false
	}
	if s.nextV == nil { // EndOfTable
		return false
	}
	if s.toPrefix == nil { // s.nextK == nil check is above
		return true
	}

	//Asc:  [from, to) AND from > to
	//Desc: [from, to) AND from < to
	cmp := bytes.Compare(s.nextV, s.toPrefix)
	return (s.orderAscend && cmp < 0) || (!s.orderAscend && cmp > 0)
}
func (s *cursorDup2iter) Next() (k, v []byte, err error) {
	select {
	case <-s.ctx.Done():
		return nil, nil, s.ctx.Err()
	default:
	}
	s.limit--
	v = s.nextV
	if err = s.advance(); err != nil {
		return nil, nil, err
	}
	return s.key, v, nil
}

func (tx *MdbxTx) ForEach(bucket string, fromPrefix []byte, walker func(k, v []byte) error) error {
	c, err := tx.Cursor(bucket)
	if err != nil {
		return err
	}
	defer c.Close()

	for k, v, err := c.Seek(fromPrefix); k != nil; k, v, err = c.Next() {
		if err != nil {
			return err
		}
		if err := walker(k, v); err != nil {
			return err
		}
	}
	return nil
}

func (tx *MdbxTx) ForPrefix(bucket string, prefix []byte, walker func(k, v []byte) error) error {
	c, err := tx.Cursor(bucket)
	if err != nil {
		return err
	}
	defer c.Close()

	for k, v, err := c.Seek(prefix); k != nil; k, v, err = c.Next() {
		if err != nil {
			return err
		}
		if !bytes.HasPrefix(k, prefix) {
			break
		}
		if err := walker(k, v); err != nil {
			return err
		}
	}
	return nil
}

func (tx *MdbxTx) ForAmount(bucket string, fromPrefix []byte, amount uint32, walker func(k, v []byte) error) error {
	if amount == 0 {
		return nil
	}
	c, err := tx.Cursor(bucket)
	if err != nil {
		return err
	}
	defer c.Close()

	for k, v, err := c.Seek(fromPrefix); k != nil && amount > 0; k, v, err = c.Next() {
		if err != nil {
			return err
		}
		if err := walker(k, v); err != nil {
			return err
		}
		amount--
	}
	return nil
}

func (tx *MdbxTx) Prefix(table string, prefix []byte) (iter.KV, error) {
	nextPrefix, ok := kv.NextSubtree(prefix)
	if !ok {
		return tx.Range(table, prefix, nil)
	}
	return tx.Range(table, prefix, nextPrefix)
}

func (tx *MdbxTx) Range(table string, fromPrefix, toPrefix []byte) (iter.KV, error) {
	return tx.RangeAscend(table, fromPrefix, toPrefix, -1)
}
func (tx *MdbxTx) RangeAscend(table string, fromPrefix, toPrefix []byte, limit int) (iter.KV, error) {
	return tx.rangeOrderLimit(table, fromPrefix, toPrefix, order.Asc, limit)
}
func (tx *MdbxTx) RangeDescend(table string, fromPrefix, toPrefix []byte, limit int) (iter.KV, error) {
	return tx.rangeOrderLimit(table, fromPrefix, toPrefix, order.Desc, limit)
}

func (tx *MdbxTx) RangeDupSort(table string, key []byte, fromPrefix, toPrefix []byte, asc order.By, limit int) (iter.KV, error) {
	s := &cursorDup2iter{ctx: tx.ctx, tx: tx, key: key, fromPrefix: fromPrefix, toPrefix: toPrefix, orderAscend: bool(asc), limit: int64(limit)}
	s.id = tx.registerStream(s)
	return s.init(table, tx)
}

func bucketSlice(b kv.TableCfg) []string {
	buckets := make([]string, 0, len(b))
	for name := range b {
		buckets = append(buckets, name)
	}
	sort.Strings(buckets)
	return buckets
}
