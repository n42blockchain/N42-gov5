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
	"bufio"
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
	// Uses 2×10^9 (2 GB) to match Geth's freezer format, not 2 GiB.
	maxFileSize = 2_000_000_000

	// writeBufferSize is the buffer size for buffered data/index file writes.
	writeBufferSize = 256 * 1024 // 256 KiB
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
	indexBuf  *bufio.Writer       // buffered index writer
	dataFiles map[uint16]*os.File // data files keyed by file number
	dataBuf   *bufio.Writer       // buffered writer for head data file
	headFile  uint16              // current data file number for appends
	headSize  int64               // tracked size of head data file (avoids Stat)

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

	// Seek to end for buffered append.
	idxFile.Seek(0, io.SeekEnd)

	t := &FreezerTable{
		name:      name,
		path:      path,
		ext:       ext,
		indexFile: idxFile,
		indexBuf:  bufio.NewWriterSize(idxFile, writeBufferSize),
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
		_ = t.tryOpenDataFile(lastIdx.fileNum)
		// Initialize head size for fast offset tracking.
		if df, ok := t.dataFiles[lastIdx.fileNum]; ok {
			if fi, err := df.Stat(); err == nil {
				t.headSize = fi.Size()
			}
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

	current := t.items.Load()
	if item < current {
		// Overwrite mode: truncate from this point, then re-append.
		if err := t.truncateHeadLocked(item); err != nil {
			return fmt.Errorf("freezer: truncate for overwrite at %d: %w", item, err)
		}
	} else if item != current {
		return fmt.Errorf("freezer: append out of order: want %d, got %d", current, item)
	}

	// Check if we need to rotate data files.
	if t.headSize+int64(len(data)) > maxFileSize {
		// Flush current data buffer before rotating.
		if t.dataBuf != nil {
			if err := t.dataBuf.Flush(); err != nil {
				return fmt.Errorf("freezer: flush before rotate: %w", err)
			}
		}
		t.headFile++
		t.headSize = 0
		t.dataBuf = nil
	}
	if t.dataBuf == nil {
		df, err := t.createDataFile(t.headFile)
		if err != nil {
			return err
		}
		// Get actual size on first use (handles pre-existing files).
		if t.headSize == 0 {
			if fi, err := df.Stat(); err == nil {
				t.headSize = fi.Size()
			}
		}
		df.Seek(0, io.SeekEnd)
		t.dataBuf = bufio.NewWriterSize(df, writeBufferSize)
	}

	offset := uint32(t.headSize)

	// Buffered data write.
	if _, err := t.dataBuf.Write(data); err != nil {
		return fmt.Errorf("freezer: write data: %w", err)
	}
	t.headSize += int64(len(data))

	// Buffered index write.
	idx := indexEntry{fileNum: t.headFile, offset: offset}
	if _, err := t.indexBuf.Write(encodeIndex(idx)); err != nil {
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
	t.mu.Lock()
	defer t.mu.Unlock()

	// Flush write buffers so ReadAt sees all data.
	if t.indexBuf != nil {
		t.indexBuf.Flush()
	}
	if t.dataBuf != nil {
		t.dataBuf.Flush()
	}

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

	// Flush buffered writes before syncing.
	if t.indexBuf != nil {
		if err := t.indexBuf.Flush(); err != nil {
			return err
		}
	}
	if t.dataBuf != nil {
		if err := t.dataBuf.Flush(); err != nil {
			return err
		}
	}
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
	// Flush buffers before closing.
	if t.indexBuf != nil {
		if err := t.indexBuf.Flush(); err != nil {
			errs = append(errs, err)
		}
	}
	if t.dataBuf != nil {
		if err := t.dataBuf.Flush(); err != nil {
			errs = append(errs, err)
		}
	}
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
	for i, data := range items {
		if err := t.Append(startItem+uint64(i), data); err != nil {
			return err
		}
	}
	return nil
}

// TruncateHead removes all items from the given item number onwards.
func (t *FreezerTable) TruncateHead(from uint64) error {
	if t.closed.Load() {
		return ErrClosed
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.truncateHeadLocked(from)
}

// truncateHeadLocked is the inner truncation logic. Caller must hold t.mu.
func (t *FreezerTable) truncateHeadLocked(from uint64) error {
	if from >= t.items.Load() {
		return nil
	}

	// Flush and reset buffers before truncation.
	if t.indexBuf != nil {
		t.indexBuf.Flush()
	}
	if t.dataBuf != nil {
		t.dataBuf.Flush()
		t.dataBuf = nil
	}
	if err := t.indexFile.Truncate(int64(from) * indexEntrySize); err != nil {
		return fmt.Errorf("freezer: truncate index: %w", err)
	}
	if _, err := t.indexFile.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	t.indexBuf.Reset(t.indexFile)
	if err := t.indexFile.Sync(); err != nil {
		return fmt.Errorf("freezer: sync after truncate: %w", err)
	}

	t.items.Store(from)
	t.headSize = 0
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
