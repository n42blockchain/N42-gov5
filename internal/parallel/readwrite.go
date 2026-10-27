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
// Three flags sit in Key's padding and the hint in IgnoreBalance's, so the
// descriptor is 128 bytes, two cache lines.
type ReadDescriptor struct {
	Key      LocationKey
	FromBase bool // true if the value was read from the base DB (no MVS entry)
	// HasValue says Value holds the bytes read (see Value).
	HasValue bool
	// HadDelta says balance deltas took part in Value (see Base).
	HadDelta bool

	WriterTx          int    // index of the transaction that provided the value (-1 if from base state)
	WriterIncarnation uint32 // incarnation of the writer tx at read time
	// IgnoreBalance marks an account read the transaction's outputs cannot
	// depend on the balance of: it never observed the balance (no
	// GetBalance/Empty on the address) and wrote at most a delta to the
	// account. Validation then compares every field but the balance, so a
	// recipient credited by preceding transactions does not invalidate it.
	IgnoreBalance bool
	// hintLo and hintHi are a 24-bit hint: one plus the place of the read
	// in its entry's version list (the versions preceding the reader) when
	// an execLean account read recorded it, else 0. Its validation looks
	// there first (beforeHint).
	hintLo uint8
	hintHi uint16
	// Value is the bytes the transaction read (nil for a missing account or
	// slot) when HasValue is set. Validation accepts a read whose version
	// moved but whose bytes did not: a re-executed writer that produced the
	// same value does not invalidate its dependants.
	Value []byte
	// Account reads (FieldBalance) compose the latest full write, or the base
	// value, with the balance deltas preceding transactions recorded after
	// it. Base keeps the base bytes for that recomposition at validation.
	Base []byte
	// entry is the store entry of Key when the reader found one, or the
	// one the transaction's own write to Key created; nil means look it
	// up. Entries are never removed from a store, so the pointer is the
	// location's for the whole block.
	entry *mvEntry
}

// maxReadHint is the largest hint the descriptor holds.
const maxReadHint = 1<<24 - 1

// setHint stores h (0: none; at most maxReadHint).
func (rd *ReadDescriptor) setHint(h uint32) {
	rd.hintLo, rd.hintHi = uint8(h), uint16(h>>8)
}

// hintPos is the place the hint names, -1 when there is none.
func (rd *ReadDescriptor) hintPos() int {
	return int(rd.hintHi)<<8 | int(rd.hintLo) - 1
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
	// A transfer writes a few keys: scan them per read instead of building
	// a map (one map per transaction was ~5% of the exec wave's
	// allocations at 163k transfers). Large write sets keep the map.
	var fullWrites map[types.Address]struct{}
	if len(rw.Writes) > markScanMaxWrites {
		for i := range rw.Writes {
			wd := &rw.Writes[i]
			if wd.Key.Field == FieldBalance && wd.Delta == nil {
				if fullWrites == nil {
					fullWrites = make(map[types.Address]struct{})
				}
				fullWrites[wd.Key.Address] = struct{}{}
			}
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
		if len(rw.Writes) > markScanMaxWrites {
			if _, full := fullWrites[rd.Key.Address]; full {
				continue
			}
		} else if rw.hasFullBalanceWrite(rd.Key.Address) {
			continue
		}
		rd.IgnoreBalance = true
	}
}

// markScanMaxWrites is the write-set size up to which MarkBalanceInsensitive
// scans the writes instead of indexing them.
const markScanMaxWrites = 16

func (rw *ReadWriteSet) hasFullBalanceWrite(addr types.Address) bool {
	for i := range rw.Writes {
		wd := &rw.Writes[i]
		if wd.Key.Field == FieldBalance && wd.Delta == nil && wd.Key.Address == addr {
			return true
		}
	}
	return false
}

// Clear resets the read and write sets for reuse.
func (rw *ReadWriteSet) Clear() {
	rw.Reads = rw.Reads[:0]
	rw.Writes = rw.Writes[:0]
}
