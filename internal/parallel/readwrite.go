// Copyright 2022-2026 The N42 Authors
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

// ReadDescriptor records a single read operation during transaction execution.
type ReadDescriptor struct {
	Key              LocationKey
	WriterTx         int    // index of the transaction that provided the value (-1 if from base state)
	WriterIncarnation uint32 // incarnation of the writer tx at read time
	FromBase         bool   // true if the value was read from the base DB (no MVS entry)
}

// WriteDescriptor records a single write operation during transaction execution.
type WriteDescriptor struct {
	Key   LocationKey
	Value []byte // serialized value; nil means delete
}

// ReadWriteSet captures all reads and writes performed by a single transaction.
// Used for validation: after execution, we check if all reads are still consistent.
type ReadWriteSet struct {
	TxIndex int
	Reads   []ReadDescriptor
	Writes  []WriteDescriptor
}

// NewReadWriteSet creates a new empty read/write set for the given transaction.
func NewReadWriteSet(txIndex int) *ReadWriteSet {
	return &ReadWriteSet{
		TxIndex: txIndex,
		Reads:   make([]ReadDescriptor, 0, 32),
		Writes:  make([]WriteDescriptor, 0, 16),
	}
}

// RecordRead adds a read to the set.
func (rw *ReadWriteSet) RecordRead(key LocationKey, writerTx int, writerIncarnation uint32, fromBase bool) {
	rw.Reads = append(rw.Reads, ReadDescriptor{
		Key:               key,
		WriterTx:          writerTx,
		WriterIncarnation: writerIncarnation,
		FromBase:          fromBase,
	})
}

// RecordWrite adds a write to the set.
func (rw *ReadWriteSet) RecordWrite(key LocationKey, value []byte) {
	rw.Writes = append(rw.Writes, WriteDescriptor{
		Key:   key,
		Value: value,
	})
}

// Clear resets the read/write set for re-execution.
func (rw *ReadWriteSet) Clear() {
	rw.Reads = rw.Reads[:0]
	rw.Writes = rw.Writes[:0]
}
