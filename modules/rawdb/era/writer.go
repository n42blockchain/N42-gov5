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

package era

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
)

// Writer creates an EraE archive file.
//
// Usage:
//
//	w, _ := NewWriter("archive.era", 1)
//	w.Append(0, blockData, receiptsData)
//	w.Finalize()
type Writer struct {
	file      *os.File
	index     []IndexEntry
	offset    int64
	networkID uint64
	finalized bool
}

// NewWriter creates a new EraE archive at the given path.
func NewWriter(path string, networkID uint64) (*Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("era: create file: %w", err)
	}

	header := EncodeHeader(networkID)
	n, err := f.Write(header)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("era: write header: %w", err)
	}

	return &Writer{
		file:      f,
		index:     make([]IndexEntry, 0, 256),
		offset:    int64(n),
		networkID: networkID,
	}, nil
}

// Append writes a block record (block data + receipts data) to the archive.
// Records must be appended in block number order.
func (w *Writer) Append(blockNumber uint64, blockData, receiptsData []byte) error {
	if w.finalized {
		return errors.New("era: writer already finalized")
	}

	// Record format: blockDataLen(8) + blockData + receiptsDataLen(8) + receiptsData
	recordOffset := w.offset

	// Write block data length and data.
	var lenBuf [8]byte
	binary.BigEndian.PutUint64(lenBuf[:], uint64(len(blockData)))
	if _, err := w.file.Write(lenBuf[:]); err != nil {
		return fmt.Errorf("era: write block data length: %w", err)
	}
	if _, err := w.file.Write(blockData); err != nil {
		return fmt.Errorf("era: write block data: %w", err)
	}

	// Write receipts data length and data.
	binary.BigEndian.PutUint64(lenBuf[:], uint64(len(receiptsData)))
	if _, err := w.file.Write(lenBuf[:]); err != nil {
		return fmt.Errorf("era: write receipts data length: %w", err)
	}
	if _, err := w.file.Write(receiptsData); err != nil {
		return fmt.Errorf("era: write receipts data: %w", err)
	}

	recordSize := int64(8 + len(blockData) + 8 + len(receiptsData))
	w.offset += recordSize

	w.index = append(w.index, IndexEntry{
		BlockNumber: blockNumber,
		FileOffset:  recordOffset,
	})

	return nil
}

// Finalize writes the index and footer, then closes the file.
// After Finalize the Writer must not be used.
func (w *Writer) Finalize() error {
	if w.finalized {
		return errors.New("era: writer already finalized")
	}
	w.finalized = true

	// Write index entries.
	var entryBuf [IndexEntrySize]byte
	for _, entry := range w.index {
		binary.BigEndian.PutUint64(entryBuf[0:8], entry.BlockNumber)
		binary.BigEndian.PutUint64(entryBuf[8:16], uint64(entry.FileOffset))
		if _, err := w.file.Write(entryBuf[:]); err != nil {
			return fmt.Errorf("era: write index entry: %w", err)
		}
	}

	// Write footer: number of index entries (uint64).
	var footer [8]byte
	binary.BigEndian.PutUint64(footer[:], uint64(len(w.index)))
	if _, err := w.file.Write(footer[:]); err != nil {
		return fmt.Errorf("era: write footer: %w", err)
	}

	return w.file.Close()
}

// Close closes the writer without finalizing. Any partial file remains on disk.
func (w *Writer) Close() error {
	if w.file == nil {
		return nil
	}
	return w.file.Close()
}
