// Copyright 2026 The N42 Authors
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

package parallel

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// ReadDescriptor records one read a transaction made during execution.
type ReadDescriptor struct {
	Key               LocationKey
	WriterTx          int    // index of the transaction that provided the value (-1 if from base state)
	WriterIncarnation uint32 // incarnation of the writer tx at read time
	FromBase          bool   // true if the value was read from the base DB (no MVS entry)
	// Value is the bytes the transaction read (nil for a missing account or
	// slot) when HasValue is set. Validation accepts a read whose version
	// moved but whose bytes did not: a re-executed writer that produced the
	// same value does not invalidate its dependants.
	Value    []byte
	HasValue bool
	// Account reads (FieldBalance) compose the latest full write, or the base
	// value, with the balance deltas preceding transactions recorded after
	// it. Base keeps the base bytes for that recomposition at validation;
	// HadDelta says deltas took part in Value.
	Base     []byte
	HadDelta bool
	// IgnoreBalance marks an account read the transaction's outputs cannot
	// depend on the balance of: it never observed the balance (no
	// GetBalance/Empty on the address) and wrote at most a delta to the
	// account. Validation then compares every field but the balance, so a
	// recipient credited by preceding transactions does not invalidate it.
	IgnoreBalance bool
}

// WriteDescriptor records one write a transaction made during execution.
type WriteDescriptor struct {
	Key   LocationKey
	Value []byte // serialized value; nil means delete
	// Delta, when non-nil, is a balance increment on an account instead of a
	// full value: commutative with other deltas, composed onto the latest
	// full value at read and apply time.
	Delta *uint256.Int
}

// ReadWriteSet is the read set and write set of one transaction execution.
type ReadWriteSet struct {
	TxIndex int
	Reads   []ReadDescriptor
	Writes  []WriteDescriptor
}

// NewReadWriteSet creates an empty read/write set for a transaction.
func NewReadWriteSet(txIndex int) *ReadWriteSet {
	return &ReadWriteSet{
		TxIndex: txIndex,
		// A transfer reads three or four locations and writes two; the old
		// 32/16 was 4 KB of garbage per transaction, 600 MB a 163k block.
		Reads:  make([]ReadDescriptor, 0, 6),
		Writes: make([]WriteDescriptor, 0, 3),
	}
}

// RecordRead records a read by version only.
func (rw *ReadWriteSet) RecordRead(key LocationKey, writerTx int, writerIncarnation uint32, fromBase bool) {
	rw.Reads = append(rw.Reads, ReadDescriptor{
		Key:               key,
		WriterTx:          writerTx,
		WriterIncarnation: writerIncarnation,
		FromBase:          fromBase,
	})
}

// RecordReadValue is RecordRead with the bytes that were read, so the
// validator can compare values when the version has moved.
func (rw *ReadWriteSet) RecordReadValue(key LocationKey, writerTx int, writerIncarnation uint32, fromBase bool, value []byte) {
	rw.Reads = append(rw.Reads, ReadDescriptor{
		Key:               key,
		WriterTx:          writerTx,
		WriterIncarnation: writerIncarnation,
		FromBase:          fromBase,
		Value:             value,
		HasValue:          true,
	})
}

// RecordAccountRead records an account read: value is the composed bytes
// the transaction saw, base the base bytes when fromBase (nil otherwise),
// hadDelta whether deltas took part.
func (rw *ReadWriteSet) RecordAccountRead(key LocationKey, writerTx int, writerIncarnation uint32, fromBase bool, value, base []byte, hadDelta bool) {
	rw.Reads = append(rw.Reads, ReadDescriptor{
		Key:               key,
		WriterTx:          writerTx,
		WriterIncarnation: writerIncarnation,
		FromBase:          fromBase,
		Value:             value,
		HasValue:          true,
		Base:              base,
		HadDelta:          hadDelta,
	})
}

// RecordWrite records a full-value write (nil deletes).
func (rw *ReadWriteSet) RecordWrite(key LocationKey, value []byte) {
	rw.Writes = append(rw.Writes, WriteDescriptor{
		Key:   key,
		Value: value,
	})
}

// RecordDeltaWrite records a balance increment on an account.
func (rw *ReadWriteSet) RecordDeltaWrite(key LocationKey, delta *uint256.Int) {
	rw.Writes = append(rw.Writes, WriteDescriptor{
		Key:   key,
		Delta: delta.Clone(),
	})
}

// MarkBalanceInsensitive flags the account reads whose balance the
// transaction's outputs cannot depend on: the address's balance was never
// observed (per observed) and the transaction wrote no full value to the
// account (a full write could carry the read balance forward).
func (rw *ReadWriteSet) MarkBalanceInsensitive(observed func(types.Address) bool) {
	var fullWrites map[types.Address]struct{}
	for i := range rw.Writes {
		wd := &rw.Writes[i]
		if wd.Key.Field == FieldBalance && wd.Delta == nil {
			if fullWrites == nil {
				fullWrites = make(map[types.Address]struct{})
			}
			fullWrites[wd.Key.Address] = struct{}{}
		}
	}
	for i := range rw.Reads {
		rd := &rw.Reads[i]
		if rd.Key.Field != FieldBalance || !rd.HasValue {
			continue
		}
		if observed(rd.Key.Address) {
			continue
		}
		if _, full := fullWrites[rd.Key.Address]; full {
			continue
		}
		rd.IgnoreBalance = true
	}
}

// Clear resets the read and write sets for reuse.
func (rw *ReadWriteSet) Clear() {
	rw.Reads = rw.Reads[:0]
	rw.Writes = rw.Writes[:0]
}
