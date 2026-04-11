// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

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

	"github.com/n42blockchain/N42/log"

	"github.com/klauspost/compress/zstd"
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

// isZstdFrame reports whether data begins with the zstd frame magic (0xFD2FB528).
func isZstdFrame(data []byte) bool {
	return len(data) >= 4 &&
		data[0] == 0x28 && data[1] == 0xB5 && data[2] == 0x2F && data[3] == 0xFD
}

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

	mu          sync.RWMutex
	indexFile   *os.File            // single index file
	indexBuf    *bufio.Writer       // buffered index writer (nil if readonly)
	dataFiles   map[uint16]*os.File // data files keyed by file number
	dataFilesRW map[uint16]bool     // tracks which files are opened RDWR (vs read-only)
	dataBuf     *bufio.Writer       // buffered writer for head data file
	headFile    uint16              // current data file number for appends
	headSize    int64               // tracked size of head data file (avoids Stat)

	items  atomic.Uint64 // total number of items stored
	closed atomic.Bool
	// readonly disables all mutating operations and opens files O_RDONLY,
	// guaranteeing the on-disk leaves data is never modified.
	readonly bool

	// Per-entry zstd compression: Append compresses, Retrieve decompresses.
	compressed bool
	zstdEnc    *zstd.Encoder
	zstdDec    *zstd.Decoder

	// Batch size for retrieveBatch O(1) lookup. Set from BatchSize.
	batchSize       int
	batchCacheStart uint64
	batchCacheEnd   uint64
	batchCacheData  [][]byte
}

// NewFreezerTable opens or creates a Geth-compatible freezer table.
// The ext parameter selects file extensions: "c" for .cidx/.cdat, "r" for .ridx/.rdat.
func NewFreezerTable(path, name, ext string) (*FreezerTable, error) {
	return newFreezerTable(path, name, ext, false)
}

// NewFreezerTableReadOnly opens an existing freezer table without ever
// touching the on-disk files. The cidx file must already exist; this
// function never creates files, never truncates partial entries, and
// rejects all subsequent mutating calls.
func NewFreezerTableReadOnly(path, name, ext string) (*FreezerTable, error) {
	return newFreezerTable(path, name, ext, true)
}

func newFreezerTable(path, name, ext string, readonly bool) (*FreezerTable, error) {
	if ext == "" {
		ext = "c"
	}
	if !readonly {
		if err := os.MkdirAll(path, 0755); err != nil {
			return nil, fmt.Errorf("freezer: mkdir %s: %w", path, err)
		}
	}

	idxPath := filepath.Join(path, fmt.Sprintf("%s.%sidx", name, ext))
	var (
		idxFile *os.File
		err     error
	)
	if readonly {
		idxFile, err = os.OpenFile(idxPath, os.O_RDONLY, 0)
	} else {
		idxFile, err = os.OpenFile(idxPath, os.O_RDWR|os.O_CREATE, 0644)
	}
	if err != nil {
		return nil, fmt.Errorf("freezer: open index %s: %w", idxPath, err)
	}

	// Seek to end for buffered append (write mode only).
	if !readonly {
		idxFile.Seek(0, io.SeekEnd)
	}

	t := &FreezerTable{
		name:        name,
		path:        path,
		ext:         ext,
		indexFile:   idxFile,
		dataFiles:   make(map[uint16]*os.File),
		dataFilesRW: make(map[uint16]bool),
		readonly:    readonly,
	}
	if !readonly {
		t.indexBuf = bufio.NewWriterSize(idxFile, writeBufferSize)
	}

	// Determine item count from index file size.
	info, err := idxFile.Stat()
	if err != nil {
		idxFile.Close()
		return nil, err
	}
	idxSize := info.Size()
	if rem := idxSize % indexEntrySize; rem != 0 {
		if readonly {
			// Read-only: just round down without modifying disk.
			idxSize -= rem
			log.Debug("Freezer (RO): ignoring partial index entry", "table", name, "ignored", rem)
		} else {
			idxSize -= rem
			if err := idxFile.Truncate(idxSize); err != nil {
				idxFile.Close()
				return nil, fmt.Errorf("freezer: truncate partial index %s: %w", name, err)
			}
			idxFile.Seek(0, io.SeekEnd)
			log.Warn("Freezer: truncated partial index entry", "table", name, "removed", rem)
		}
	}
	t.items.Store(uint64(idxSize) / indexEntrySize)

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

// NewFreezerTableCompressed opens a freezer table with per-entry zstd compression.
// If per-file dictionaries exist ({table}.NNNN.zdict, created by CompactTable),
// they are loaded for both encoding and decoding.
func NewFreezerTableCompressed(path, name, ext string) (*FreezerTable, error) {
	return newFreezerTableCompressed(path, name, ext, false)
}

// NewFreezerTableCompressedReadOnly opens an existing compressed freezer table
// without ever modifying its files. See NewFreezerTableReadOnly.
func NewFreezerTableCompressedReadOnly(path, name, ext string) (*FreezerTable, error) {
	return newFreezerTableCompressed(path, name, ext, true)
}

func newFreezerTableCompressed(path, name, ext string, readonly bool) (*FreezerTable, error) {
	t, err := newFreezerTable(path, name, ext, readonly)
	if err != nil {
		return nil, err
	}
	t.compressed = true
	// Auto-detect batch format: check if two consecutive mid-file entries
	// share the same (fileNum, offset). Only need >= 2 items.
	if t.items.Load() >= 2 {
		mid := t.items.Load() / 2
		if mid == 0 {
			mid = 1
		}
		if i1, err1 := t.readIndex(mid - 1); err1 == nil {
			if i2, err2 := t.readIndex(mid); err2 == nil {
				if i1.fileNum == i2.fileNum && i1.offset == i2.offset {
					t.batchSize = BatchSize
				}
			}
		}
	}

	// Load all per-file dictionaries. The last one (head file) is reused
	// for the encoder so we don't read it twice.
	var allDicts [][]byte
	for i := 0; i <= int(t.headFile); i++ {
		dictPath := filepath.Join(path, fmt.Sprintf("%s.%04d.zdict", name, i))
		if d, err := os.ReadFile(dictPath); err == nil && len(d) > 0 {
			allDicts = append(allDicts, d)
		}
	}

	// Build encoder — use head file dict if available.
	encOpts := []zstd.EOption{zstd.WithEncoderLevel(zstd.SpeedBetterCompression)}
	if len(allDicts) > 0 {
		encOpts = append(encOpts, zstd.WithEncoderDict(allDicts[len(allDicts)-1]))
	}
	t.zstdEnc, err = zstd.NewWriter(nil, encOpts...)
	if err != nil {
		t.Close()
		return nil, fmt.Errorf("freezer: zstd encoder: %w", err)
	}

	// Build decoder with all dictionaries.
	decOpts := []zstd.DOption{}
	if len(allDicts) > 0 {
		decOpts = append(decOpts, zstd.WithDecoderDicts(allDicts...))
	}
	t.zstdDec, err = zstd.NewReader(nil, decOpts...)
	if err != nil {
		t.Close()
		return nil, fmt.Errorf("freezer: zstd decoder: %w", err)
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
	if t.readonly {
		return fmt.Errorf("freezer: table %s is read-only", t.name)
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	current := t.items.Load()
	if item < current {
		// Overwrite mode: truncate from this point, then re-append.
		t.batchCacheData = nil // invalidate batch cache
		if err := t.truncateHeadLocked(item); err != nil {
			return fmt.Errorf("freezer: truncate for overwrite at %d: %w", item, err)
		}
	} else if item != current {
		return fmt.Errorf("freezer: append out of order: want %d, got %d", current, item)
	}

	// Compress if enabled. Use compressed only if it's actually smaller.
	if t.compressed && t.zstdEnc != nil && len(data) > 0 {
		compressed := t.zstdEnc.EncodeAll(data, make([]byte, 0, len(data)/2))
		if len(compressed) < len(data) {
			data = compressed
		}
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

// SetBatchSize enables batch read mode. Once set, Retrieve uses O(1) batch lookup.
func (t *FreezerTable) setBatchSize(bs int) { t.batchSize = bs }

// ForceBatchSize explicitly sets the batch size for reading. Use when the
// auto-detection in NewFreezerTableCompressed fails (e.g. mid-point happens
// to land on a batch boundary where consecutive entries have different offsets).
func (t *FreezerTable) ForceBatchSize(bs int) { t.batchSize = bs }

// SetCompressed enables compressed batch read mode with zstd decoder.
func (t *FreezerTable) SetCompressed(v bool) {
	t.compressed = v
	if v && t.zstdDec == nil {
		t.zstdDec, _ = zstd.NewReader(nil)
	}
}

// AppendBatchBlob writes a pre-encoded batch blob and creates N cidx entries
// all pointing to the same offset. Used for batch compression.
func (t *FreezerTable) AppendBatchBlob(startItem uint64, count int, blob []byte) error {
	if t.closed.Load() {
		return ErrClosed
	}
	if t.readonly {
		return fmt.Errorf("freezer: table %s is read-only", t.name)
	}
	if count <= 0 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	current := t.items.Load()
	if startItem < current {
		t.batchCacheData = nil
		if err := t.truncateHeadLocked(startItem); err != nil {
			return fmt.Errorf("freezer: truncate for batch at %d: %w", startItem, err)
		}
	} else if startItem != current {
		return fmt.Errorf("freezer: batch append out of order: want %d, got %d", current, startItem)
	}

	// Rotate if needed.
	if t.headSize+int64(len(blob)) > maxFileSize {
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
		if t.headSize == 0 {
			if fi, err := df.Stat(); err == nil {
				t.headSize = fi.Size()
			}
		}
		df.Seek(0, io.SeekEnd)
		t.dataBuf = bufio.NewWriterSize(df, writeBufferSize)
	}

	// Record offset BEFORE writing blob — all N entries share this offset.
	offset := uint32(t.headSize)

	// Write blob once.
	if len(blob) > 0 {
		if _, err := t.dataBuf.Write(blob); err != nil {
			return fmt.Errorf("freezer: write batch data: %w", err)
		}
		t.headSize += int64(len(blob))
	}

	// Write N cidx entries all pointing to the same offset.
	idx := encodeIndex(indexEntry{fileNum: t.headFile, offset: offset})
	for i := 0; i < count; i++ {
		if _, err := t.indexBuf.Write(idx); err != nil {
			return fmt.Errorf("freezer: write batch index: %w", err)
		}
	}
	t.items.Add(uint64(count))
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

	// Batch mode: if batchSize is set, always use batch retrieval.
	// All entries in a batch share the same cidx offset.
	if t.compressed && t.batchSize > 0 {
		return t.retrieveBatch(item)
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

	var data []byte

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
		data = make([]byte, size)
		if _, err := df.ReadAt(data, int64(idx.offset)); err != nil {
			return nil, fmt.Errorf("freezer: read data item %d: %w", item, err)
		}
	} else {
		// Cross-file boundary: item spans from idx.fileNum to endFileNum.
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

		data = make([]byte, totalSize)

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
	}

	// Decompress if enabled. Detect zstd magic to skip raw entries.
	// On decompression failure, fall back to raw data — the entry may
	// not be compressed even though its first bytes match the magic
	// (written by an older version without per-entry compression).
	if t.compressed && t.zstdDec != nil && isZstdFrame(data) {
		if decompressed, err := t.zstdDec.DecodeAll(data, nil); err == nil {
			data = decompressed
		}
	}

	return data, nil
}


// retrieveBatch handles reading an item from a batch-compressed region.
// Batch format: consecutive cidx entries share the same (fileNum, offset).
// The first entry's offset points to zstd_compressed([len0:4][data0][len1:4][data1]...).
// Caller must hold t.mu.
func (t *FreezerTable) retrieveBatch(item uint64) ([]byte, error) {
	bs := uint64(t.batchSize)
	batchStart := (item / bs) * bs
	batchEnd := batchStart + bs
	total := t.items.Load()
	if batchEnd > total {
		batchEnd = total
	}

	// Batch cache hit.
	if item >= t.batchCacheStart && item < t.batchCacheEnd && t.batchCacheData != nil {
		localIdx := int(item - t.batchCacheStart)
		if localIdx < len(t.batchCacheData) {
			return t.batchCacheData[localIdx], nil
		}
	}

	// Read the batch start entry's index to get cdat offset.
	batchIdx, err := t.readIndex(batchStart)
	if err != nil {
		return nil, err
	}

	// Find batch end offset in cdat.
	var compEnd uint32
	if batchEnd < total {
		endIdx, err := t.readIndex(batchEnd)
		if err != nil {
			return nil, err
		}
		if endIdx.fileNum == batchIdx.fileNum {
			compEnd = endIdx.offset
		} else {
			sz, err := t.getDataFileSize(batchIdx.fileNum)
			if err != nil {
				return nil, err
			}
			compEnd = sz
		}
	} else {
		sz, err := t.getDataFileSize(batchIdx.fileNum)
		if err != nil {
			return nil, err
		}
		compEnd = sz
	}

	compSize := compEnd - batchIdx.offset
	if compSize == 0 {
		return []byte{}, nil
	}

	df, err := t.openDataFileRO(batchIdx.fileNum)
	if err != nil {
		return nil, err
	}
	comp := make([]byte, compSize)
	if _, err := df.ReadAt(comp, int64(batchIdx.offset)); err != nil {
		return nil, fmt.Errorf("freezer: read batch at %d: %w", item, err)
	}

	// Decompress.
	var raw []byte
	if isZstdFrame(comp) {
		raw, err = t.zstdDec.DecodeAll(comp, nil)
		if err != nil {
			return nil, fmt.Errorf("freezer: decompress batch at %d: %w", item, err)
		}
	} else {
		raw = comp
	}

	// Parse length-prefixed entries: [len:4LE][data]...
	batchCount := int(batchEnd - batchStart)
	entries := make([][]byte, 0, batchCount)
	pos := 0
	for pos+4 <= len(raw) && len(entries) < batchCount {
		entryLen := int(binary.LittleEndian.Uint32(raw[pos:]))
		pos += 4
		if pos+entryLen > len(raw) {
			break
		}
		entries = append(entries, raw[pos:pos+entryLen])
		pos += entryLen
	}

	// Cache the batch.
	t.batchCacheStart = batchStart
	t.batchCacheEnd = batchStart + uint64(len(entries))
	t.batchCacheData = entries

	localIdx := int(item - batchStart)
	if localIdx >= len(entries) {
		return []byte{}, nil
	}
	return entries[localIdx], nil
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
	if t.readonly {
		return nil // nothing to flush
	}
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
	if t.zstdEnc != nil {
		t.zstdEnc.Close()
	}
	if t.zstdDec != nil {
		t.zstdDec.Close()
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
// If a read-only handle exists in cache (from openDataFileRO), it is closed
// and replaced with a read-write handle.
func (t *FreezerTable) createDataFile(num uint16) (*os.File, error) {
	if f, ok := t.dataFiles[num]; ok {
		if t.dataFilesRW[num] {
			return f, nil // already RDWR
		}
		// Close read-only handle before re-opening as RDWR.
		f.Close()
		delete(t.dataFiles, num)
	}
	name := filepath.Join(t.path, fmt.Sprintf("%s.%04d.%sdat", t.name, num, t.ext))
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("freezer: create data file %d: %w", num, err)
	}
	t.dataFiles[num] = f
	t.dataFilesRW[num] = true
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
	if t.readonly {
		return fmt.Errorf("freezer: table %s is read-only", t.name)
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

	// Invalidate batch cache — it may hold entries being truncated.
	t.batchCacheData = nil

	// Read the first removed item's cdat offset BEFORE truncating cidx.
	// This tells us where orphaned data starts in the cdat file.
	var removedIdx indexEntry
	var haveRemovedIdx bool
	if from < t.items.Load() {
		idx, err := t.readIndex(from)
		if err == nil {
			removedIdx = idx
			haveRemovedIdx = true
		}
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
	if from == 0 {
		t.headFile = 0
		t.headSize = 0
		return nil
	}

	lastIdx, err := t.readIndex(from - 1)
	if err != nil {
		return err
	}
	t.headFile = lastIdx.fileNum

	// Truncate cdat to remove orphaned data after the last retained batch.
	// Without this, orphaned blobs corrupt retrieveBatch range calculations.
	if haveRemovedIdx {
		if removedIdx.fileNum == lastIdx.fileNum {
			// Same file: truncate at the orphaned data start offset.
			df, err := t.createDataFile(removedIdx.fileNum)
			if err == nil {
				df.Truncate(int64(removedIdx.offset))
			}
			t.headSize = int64(removedIdx.offset)
		} else {
			// Cross-file: removed items are in a later file. Delete every
			// orphaned data file with fileNum >= removedIdx.fileNum, then
			// reset headSize from the last retained file.
			//
			// We scan the directory rather than iterate a fixed window so a
			// single TruncateHead can clean up an arbitrary number of
			// segments — the previous bounded loop left orphans behind when
			// the truncation crossed more than ~10 .cdat files at once,
			// which accumulated unbounded waste over kill+resume cycles.
			if err := t.deleteOrphanFilesLocked(removedIdx.fileNum); err != nil {
				return err
			}
			df, err := t.createDataFile(lastIdx.fileNum)
			if err == nil {
				if fi, err := df.Stat(); err == nil {
					t.headSize = fi.Size()
				}
			}
		}
	} else {
		// Couldn't read removed index — use file stat as fallback.
		df, err := t.createDataFile(lastIdx.fileNum)
		if err == nil {
			if fi, err := df.Stat(); err == nil {
				t.headSize = fi.Size()
			}
		}
	}
	return nil
}

// deleteOrphanFilesLocked removes every <name>.NNNN.<ext>dat file in the
// table directory whose segment number is >= startFileNum, closing any
// open handles first. Caller must hold t.mu.
func (t *FreezerTable) deleteOrphanFilesLocked(startFileNum uint16) error {
	entries, err := os.ReadDir(t.path)
	if err != nil {
		return fmt.Errorf("freezer: read dir for orphan cleanup: %w", err)
	}
	prefix := t.name + "."
	suffix := "." + t.ext + "dat"
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fname := e.Name()
		if len(fname) < len(prefix)+4+len(suffix) {
			continue
		}
		if fname[:len(prefix)] != prefix || fname[len(fname)-len(suffix):] != suffix {
			continue
		}
		var parsed uint32
		// %04d width is the convention; accept any width that parses cleanly.
		if _, err := fmt.Sscanf(fname[len(prefix):len(fname)-len(suffix)], "%d", &parsed); err != nil {
			continue
		}
		if parsed < uint32(startFileNum) || parsed > 0xffff {
			continue
		}
		fnum := uint16(parsed)
		if f, ok := t.dataFiles[fnum]; ok {
			f.Close()
			delete(t.dataFiles, fnum)
			delete(t.dataFilesRW, fnum)
		}
		if err := os.Remove(filepath.Join(t.path, fname)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("freezer: remove orphan %s: %w", fname, err)
		}
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
