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
	// indexEntrySize is the byte size of each Geth-compatible index entry:
	// fileNum(2B BE) + offset(4B BE) = 6 bytes.
	indexEntrySize = 6

	// maxFileSize is the maximum size of a single data file before rotation.
	maxFileSize = 2 * 1024 * 1024 * 1024 // 2 GiB
)

var (
	ErrOutOfBounds = errors.New("freezer: item out of bounds")
	ErrClosed      = errors.New("freezer: table closed")
	ErrPruned      = errors.New("freezer: data pruned")
)

// indexEntry represents one item's position in the data files.
// Geth cidx format: 2-byte file number + 4-byte offset, both big-endian.
// Item size is derived from the next entry's offset, not stored explicitly.
type indexEntry struct {
	fileNum uint16 // which data file the item lives in
	offset  uint32 // byte offset within that data file
}

func encodeIndex(e indexEntry) []byte {
	var buf [indexEntrySize]byte
	binary.BigEndian.PutUint16(buf[0:2], e.fileNum)
	binary.BigEndian.PutUint32(buf[2:6], e.offset)
	return buf[:]
}

func decodeIndex(buf []byte) indexEntry {
	return indexEntry{
		fileNum: binary.BigEndian.Uint16(buf[0:2]),
		offset:  binary.BigEndian.Uint32(buf[2:6]),
	}
}

// FreezerTable represents a single append-only data table backed by
// an index file and one or more data files. Items are addressed by
// sequential number starting from 0.
//
// File layout (Geth-compatible, flat in base directory):
//
//	{name}.cidx          — index file (6 bytes per entry)
//	{name}.NNNN.cdat     — data files (2 GiB max, rotated)
//
// For fixed-size tables (hashes, diffs), use .ridx/.rdat via ext="r".
type FreezerTable struct {
	name string // table name (e.g. "headers")
	path string // base directory (all files live here)
	ext  string // "c" for cidx/cdat, "r" for ridx/rdat

	mu        sync.RWMutex
	indexFile *os.File            // single index file
	dataFiles map[uint16]*os.File // data files keyed by file number
	headFile  uint16              // current data file number for appends

	items  atomic.Uint64 // total number of items stored
	closed atomic.Bool
}

// NewFreezerTable opens or creates a Geth-compatible freezer table.
// The ext parameter selects file extensions: "c" for .cidx/.cdat, "r" for .ridx/.rdat.
func NewFreezerTable(path, name, ext string) (*FreezerTable, error) {
	if ext == "" {
		ext = "c"
	}
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, fmt.Errorf("freezer: mkdir %s: %w", path, err)
	}

	idxPath := filepath.Join(path, fmt.Sprintf("%s.%sidx", name, ext))
	idxFile, err := os.OpenFile(idxPath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("freezer: open index %s: %w", idxPath, err)
	}

	t := &FreezerTable{
		name:      name,
		path:      path,
		ext:       ext,
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
		// Try to open head file; ignore ErrNotExist (pruned).
		_ = t.tryOpenDataFile(lastIdx.fileNum)
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
	df, err := t.createDataFile(t.headFile)
	if err != nil {
		return err
	}
	info, err := df.Stat()
	if err != nil {
		return err
	}
	if info.Size()+int64(len(data)) > maxFileSize {
		t.headFile++
		df, err = t.createDataFile(t.headFile)
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
	idx := indexEntry{fileNum: t.headFile, offset: offset}
	if _, err := t.indexFile.Write(encodeIndex(idx)); err != nil {
		return fmt.Errorf("freezer: write index: %w", err)
	}

	t.items.Add(1)
	return nil
}

// Retrieve reads one item by its sequential number.
// Returns ErrPruned if the data file has been deleted (normal after prune).
// Handles items that span file boundaries (data split across two cdat files).
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

	// Determine the end boundary (next item's file and offset).
	var endFileNum uint16
	var endOffset uint32
	if item+1 < t.items.Load() {
		next, err := t.readIndex(item + 1)
		if err != nil {
			return nil, err
		}
		endFileNum = next.fileNum
		endOffset = next.offset
	} else {
		// Last item: extends to end of its data file.
		endFileNum = idx.fileNum
		sz, err := t.getDataFileSize(idx.fileNum)
		if err != nil {
			return nil, err
		}
		endOffset = sz
	}

	if idx.fileNum == endFileNum {
		// Same file: simple read.
		size := endOffset - idx.offset
		if size == 0 {
			return []byte{}, nil
		}
		df, err := t.openDataFileRO(idx.fileNum)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, ErrPruned
			}
			return nil, err
		}
		data := make([]byte, size)
		if _, err := df.ReadAt(data, int64(idx.offset)); err != nil {
			return nil, fmt.Errorf("freezer: read data item %d: %w", item, err)
		}
		return data, nil
	}

	// Cross-file boundary: item spans from idx.fileNum to endFileNum.
	// Read tail of current file + head of next file.
	tailSize, err := t.getDataFileSize(idx.fileNum)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrPruned
		}
		return nil, err
	}
	tailLen := tailSize - idx.offset
	totalSize := tailLen + endOffset

	if totalSize == 0 {
		return []byte{}, nil
	}

	data := make([]byte, totalSize)

	// Read tail from current file.
	if tailLen > 0 {
		df, err := t.openDataFileRO(idx.fileNum)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, ErrPruned
			}
			return nil, err
		}
		if _, err := df.ReadAt(data[:tailLen], int64(idx.offset)); err != nil {
			return nil, fmt.Errorf("freezer: read tail item %d file %d: %w", item, idx.fileNum, err)
		}
	}

	// Read head from next file.
	if endOffset > 0 {
		df2, err := t.openDataFileRO(endFileNum)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, ErrPruned
			}
			return nil, err
		}
		if _, err := df2.ReadAt(data[tailLen:], 0); err != nil {
			return nil, fmt.Errorf("freezer: read head item %d file %d: %w", item, endFileNum, err)
		}
	}

	return data, nil
}

// itemSize calculates the byte length of an item by looking at the next
// entry's offset. For the last item or cross-file items, uses data file size.
// Caller must hold at least RLock.
func (t *FreezerTable) itemSize(item uint64, idx indexEntry) (uint32, error) {
	if item+1 < t.items.Load() {
		next, err := t.readIndex(item + 1)
		if err != nil {
			return 0, err
		}
		if next.fileNum == idx.fileNum {
			return next.offset - idx.offset, nil
		}
		// Cross file boundary: item extends to end of current file.
		sz, err := t.getDataFileSize(idx.fileNum)
		if err != nil {
			return 0, err
		}
		return sz - idx.offset, nil
	}
	// Last item: extends to end of its data file.
	sz, err := t.getDataFileSize(idx.fileNum)
	if err != nil {
		return 0, err
	}
	return sz - idx.offset, nil
}

// getDataFileSize returns the byte size of a data file.
// Caller must hold at least RLock.
func (t *FreezerTable) getDataFileSize(fileNum uint16) (uint32, error) {
	df, err := t.openDataFileRO(fileNum)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, ErrPruned
		}
		return 0, err
	}
	info, err := df.Stat()
	if err != nil {
		return 0, err
	}
	return uint32(info.Size()), nil
}

// Has checks whether an item exists in the index (may still be pruned on disk).
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

// createDataFile opens or creates a data file for writing. Caller must hold Lock.
func (t *FreezerTable) createDataFile(num uint16) (*os.File, error) {
	if f, ok := t.dataFiles[num]; ok {
		return f, nil
	}
	name := filepath.Join(t.path, fmt.Sprintf("%s.%04d.%sdat", t.name, num, t.ext))
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("freezer: create data file %d: %w", num, err)
	}
	t.dataFiles[num] = f
	return f, nil
}

// openDataFileRO opens a data file read-only. Does NOT create the file.
// Returns os.ErrNotExist if pruned. Caller must hold at least RLock.
func (t *FreezerTable) openDataFileRO(num uint16) (*os.File, error) {
	if f, ok := t.dataFiles[num]; ok {
		return f, nil
	}
	name := filepath.Join(t.path, fmt.Sprintf("%s.%04d.%sdat", t.name, num, t.ext))
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	t.dataFiles[num] = f
	return f, nil
}

// tryOpenDataFile attempts to open a data file, ignoring errors.
// Used during initialization when files may be pruned. Caller must hold at least RLock.
func (t *FreezerTable) tryOpenDataFile(num uint16) error {
	_, err := t.openDataFileRO(num)
	return err
}

// AppendBatch appends multiple items atomically.
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
		df, err := t.createDataFile(t.headFile)
		if err != nil {
			return err
		}
		info, err := df.Stat()
		if err != nil {
			return err
		}
		if info.Size()+int64(len(data)) > maxFileSize {
			t.headFile++
			df, err = t.createDataFile(t.headFile)
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
		idx := indexEntry{fileNum: t.headFile, offset: offset}
		if _, err := t.indexFile.Write(encodeIndex(idx)); err != nil {
			return fmt.Errorf("freezer: batch write index item %d: %w", startItem+uint64(i), err)
		}
	}
	t.items.Add(uint64(len(items)))
	return nil
}

// TruncateHead removes all items from the given item number onwards.
func (t *FreezerTable) TruncateHead(from uint64) error {
	if t.closed.Load() {
		return ErrClosed
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if from >= t.items.Load() {
		return nil
	}

	if err := t.indexFile.Truncate(int64(from) * indexEntrySize); err != nil {
		return fmt.Errorf("freezer: truncate index: %w", err)
	}
	if _, err := t.indexFile.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	if err := t.indexFile.Sync(); err != nil {
		return fmt.Errorf("freezer: sync after truncate: %w", err)
	}

	t.items.Store(from)

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

// WriteMeta writes the item count to the .meta file.
func (t *FreezerTable) WriteMeta() error {
	metaPath := filepath.Join(t.path, t.name+".meta")
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], t.items.Load())
	return os.WriteFile(metaPath, buf[:], 0644)
}

// ReadMeta reads the item count from the .meta file.
func (t *FreezerTable) ReadMeta() (uint64, error) {
	metaPath := filepath.Join(t.path, t.name+".meta")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	if len(data) < 8 {
		return 0, nil
	}
	return binary.LittleEndian.Uint64(data), nil
}
