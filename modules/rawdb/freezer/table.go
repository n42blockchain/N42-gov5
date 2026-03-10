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

package freezer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

const (
	// indexEntrySize is the byte size of each index entry: offset(8) + size(4).
	indexEntrySize = 12

	// maxFileSize is the maximum size of a single data file before rotation.
	maxFileSize = 2 * 1024 * 1024 * 1024 // 2 GiB
)

var (
	ErrOutOfBounds = errors.New("freezer: item out of bounds")
	ErrClosed      = errors.New("freezer: table closed")
)

// indexEntry represents one item's position in the data files.
type indexEntry struct {
	fileNum uint16 // which data file the item lives in
	offset  uint32 // byte offset within that data file
	size    uint32 // byte length of the item
}

func encodeIndex(e indexEntry) []byte {
	var buf [indexEntrySize]byte
	binary.BigEndian.PutUint16(buf[0:2], e.fileNum)
	// Use 6 bytes for offset to handle files close to 2GiB
	binary.BigEndian.PutUint16(buf[2:4], uint16(e.offset>>16))
	binary.BigEndian.PutUint16(buf[4:6], uint16(e.offset))
	binary.BigEndian.PutUint32(buf[6:10], e.size)
	// 2 bytes padding
	return buf[:]
}

func decodeIndex(buf []byte) indexEntry {
	return indexEntry{
		fileNum: binary.BigEndian.Uint16(buf[0:2]),
		offset:  uint32(binary.BigEndian.Uint16(buf[2:4]))<<16 | uint32(binary.BigEndian.Uint16(buf[4:6])),
		size:    binary.BigEndian.Uint32(buf[6:10]),
	}
}

// FreezerTable represents a single append-only data table backed by
// an index file and one or more data files. Items are addressed by
// sequential number starting from 0.
type FreezerTable struct {
	name string // table name (e.g. "headers")
	path string // base directory

	mu        sync.RWMutex
	indexFile *os.File            // single index file
	dataFiles map[uint16]*os.File // data files keyed by file number
	headFile  uint16              // current data file number for appends

	items  atomic.Uint64 // total number of items stored
	closed atomic.Bool
}

// NewFreezerTable opens or creates a freezer table.
func NewFreezerTable(path, name string) (*FreezerTable, error) {
	tablePath := filepath.Join(path, name)
	if err := os.MkdirAll(tablePath, 0755); err != nil {
		return nil, fmt.Errorf("freezer: mkdir %s: %w", tablePath, err)
	}

	idxFile, err := os.OpenFile(filepath.Join(tablePath, "index.dat"), os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("freezer: open index: %w", err)
	}

	t := &FreezerTable{
		name:      name,
		path:      tablePath,
		indexFile: idxFile,
		dataFiles: make(map[uint16]*os.File),
	}

	// Determine item count from index file size.
	info, err := idxFile.Stat()
	if err != nil {
		idxFile.Close()
		return nil, err
	}
	t.items.Store(uint64(info.Size()) / indexEntrySize)

	// Open the head data file if items exist.
	if t.items.Load() > 0 {
		lastIdx, err := t.readIndex(t.items.Load() - 1)
		if err != nil {
			idxFile.Close()
			return nil, err
		}
		t.headFile = lastIdx.fileNum
		if _, err := t.openDataFile(lastIdx.fileNum); err != nil {
			idxFile.Close()
			return nil, err
		}
	} else {
		// Open file 0 for new table.
		if _, err := t.openDataFile(0); err != nil {
			idxFile.Close()
			return nil, err
		}
	}

	return t, nil
}

// Items returns the total number of items in the table.
func (t *FreezerTable) Items() uint64 {
	return t.items.Load()
}

// Append adds a data item to the table. The item number must equal Items()
// (i.e., strictly sequential appends only).
func (t *FreezerTable) Append(item uint64, data []byte) error {
	if t.closed.Load() {
		return ErrClosed
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if item != t.items.Load() {
		return fmt.Errorf("freezer: append out of order: want %d, got %d", t.items.Load(), item)
	}

	// Check if we need to rotate data files.
	df, err := t.openDataFile(t.headFile)
	if err != nil {
		return err
	}
	info, err := df.Stat()
	if err != nil {
		return err
	}
	if info.Size()+int64(len(data)) > maxFileSize {
		t.headFile++
		df, err = t.openDataFile(t.headFile)
		if err != nil {
			return err
		}
		info, err = df.Stat()
		if err != nil {
			return err
		}
	}

	offset := uint32(info.Size())

	// Write data.
	if _, err := df.Write(data); err != nil {
		return fmt.Errorf("freezer: write data: %w", err)
	}

	// Write index entry.
	idx := indexEntry{fileNum: t.headFile, offset: offset, size: uint32(len(data))}
	if _, err := t.indexFile.Write(encodeIndex(idx)); err != nil {
		return fmt.Errorf("freezer: write index: %w", err)
	}

	t.items.Add(1)
	return nil
}

// Retrieve reads one item by its sequential number.
func (t *FreezerTable) Retrieve(item uint64) ([]byte, error) {
	if t.closed.Load() {
		return nil, ErrClosed
	}
	t.mu.RLock()
	defer t.mu.RUnlock()

	if item >= t.items.Load() {
		return nil, ErrOutOfBounds
	}

	idx, err := t.readIndex(item)
	if err != nil {
		return nil, err
	}

	df, err := t.openDataFile(idx.fileNum)
	if err != nil {
		return nil, err
	}

	data := make([]byte, idx.size)
	if _, err := df.ReadAt(data, int64(idx.offset)); err != nil {
		return nil, fmt.Errorf("freezer: read data item %d: %w", item, err)
	}
	return data, nil
}

// Has checks whether an item exists.
func (t *FreezerTable) Has(item uint64) bool {
	return item < t.items.Load()
}

// Sync flushes all data and index files to disk.
func (t *FreezerTable) Sync() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.indexFile.Sync(); err != nil {
		return err
	}
	for _, f := range t.dataFiles {
		if err := f.Sync(); err != nil {
			return err
		}
	}
	return nil
}

// Close closes all file handles.
func (t *FreezerTable) Close() error {
	if t.closed.Swap(true) {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	var errs []error
	if err := t.indexFile.Close(); err != nil {
		errs = append(errs, err)
	}
	for _, f := range t.dataFiles {
		if err := f.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// readIndex reads the index entry for the given item. Caller must hold at least RLock.
func (t *FreezerTable) readIndex(item uint64) (indexEntry, error) {
	var buf [indexEntrySize]byte
	if _, err := t.indexFile.ReadAt(buf[:], int64(item)*indexEntrySize); err != nil {
		return indexEntry{}, fmt.Errorf("freezer: read index item %d: %w", item, err)
	}
	return decodeIndex(buf[:]), nil
}

// openDataFile opens (or returns already-open) data file by number. Caller must hold lock.
func (t *FreezerTable) openDataFile(num uint16) (*os.File, error) {
	if f, ok := t.dataFiles[num]; ok {
		return f, nil
	}
	name := filepath.Join(t.path, fmt.Sprintf("data.%04d", num))
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("freezer: open data file %d: %w", num, err)
	}
	t.dataFiles[num] = f
	return f, nil
}

// AppendBatch appends multiple items atomically. Returns the first item number.
func (t *FreezerTable) AppendBatch(startItem uint64, items [][]byte) error {
	if t.closed.Load() {
		return ErrClosed
	}
	if len(items) == 0 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if startItem != t.items.Load() {
		return fmt.Errorf("freezer: batch append out of order: want %d, got %d", t.items.Load(), startItem)
	}

	for i, data := range items {
		df, err := t.openDataFile(t.headFile)
		if err != nil {
			return err
		}
		info, err := df.Stat()
		if err != nil {
			return err
		}
		if info.Size()+int64(len(data)) > maxFileSize {
			t.headFile++
			df, err = t.openDataFile(t.headFile)
			if err != nil {
				return err
			}
			info, err = df.Stat()
			if err != nil {
				return err
			}
		}

		offset := uint32(info.Size())
		if _, err := df.Write(data); err != nil {
			return fmt.Errorf("freezer: batch write data item %d: %w", startItem+uint64(i), err)
		}
		idx := indexEntry{fileNum: t.headFile, offset: offset, size: uint32(len(data))}
		if _, err := t.indexFile.Write(encodeIndex(idx)); err != nil {
			return fmt.Errorf("freezer: batch write index item %d: %w", startItem+uint64(i), err)
		}
	}
	t.items.Add(uint64(len(items)))
	return nil
}

// TruncateHead removes all items from the given item number onwards.
// Used to handle chain reorgs affecting frozen blocks.
func (t *FreezerTable) TruncateHead(from uint64) error {
	if t.closed.Load() {
		return ErrClosed
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if from >= t.items.Load() {
		return nil
	}

	// Truncate index file.
	if err := t.indexFile.Truncate(int64(from) * indexEntrySize); err != nil {
		return fmt.Errorf("freezer: truncate index: %w", err)
	}
	// Seek to end after truncation.
	if _, err := t.indexFile.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	// Sync to ensure truncation is persisted.
	if err := t.indexFile.Sync(); err != nil {
		return fmt.Errorf("freezer: sync after truncate: %w", err)
	}

	t.items.Store(from)

	// Update head file if needed.
	if from > 0 {
		lastIdx, err := t.readIndex(from - 1)
		if err != nil {
			return err
		}
		t.headFile = lastIdx.fileNum
	} else {
		t.headFile = 0
	}

	return nil
}
