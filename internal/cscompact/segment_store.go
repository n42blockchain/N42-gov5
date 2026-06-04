// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// segment_store.go — unified chain archive file management.
//
// All chain data lives in a single flat directory:
//
//	{chainDir}/
//	├── {prefix}.cidx              # master index [12B per segment]
//	├── {prefix}.0000.cdat         # data frames ≤2GB, auto-rotate
//	├── {prefix}.0001.cdat
//	└── ...
//
// cidx entry: [2B fileNum LE][2B flags LE][4B datOffset LE][4B riOffset LE]
// cdat frame (batch):   [4B size LE][compressed bytes]
// cdat frame (indexed): [4B datSize LE][datBytes][4B riSize LE][riBytes]

package cscompact

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/n42blockchain/N42/lib/mmap"
	"github.com/n42blockchain/N42/lib/recsplit"
)

const (
	segStoreMaxFileSize = 2_000_000_000 // 2 GB
	segIdxEntrySize     = 12            // fileNum(2) + flags(2) + datOff(4) + riOff(4)
)

// SegmentStoreWriter writes chain archive segments.
type SegmentStoreWriter struct {
	dir      string
	prefix   string
	idxFile  *os.File
	headFile uint16
	headSize int64
}

// NewSegmentStoreWriter opens or creates a segment store for the given prefix.
// Files: {dir}/{prefix}.cidx + {dir}/{prefix}.NNNN.cdat
func NewSegmentStoreWriter(dir, prefix string) (*SegmentStoreWriter, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	idxPath := filepath.Join(dir, prefix+".cidx")
	idxFile, err := os.OpenFile(idxPath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, err
	}

	fi, _ := idxFile.Stat()
	existingSegs := fi.Size() / segIdxEntrySize
	var headFile uint16
	var headSize int64
	if existingSegs > 0 {
		var lastEntry [segIdxEntrySize]byte
		idxFile.ReadAt(lastEntry[:], (existingSegs-1)*segIdxEntrySize)
		headFile = binary.LittleEndian.Uint16(lastEntry[0:2])
		cdatPath := filepath.Join(dir, fmt.Sprintf("%s.%04d.cdat", prefix, headFile))
		if dfi, err := os.Stat(cdatPath); err == nil {
			headSize = dfi.Size()
		}
	}
	idxFile.Seek(0, 2)

	return &SegmentStoreWriter{
		dir: dir, prefix: prefix, idxFile: idxFile,
		headFile: headFile, headSize: headSize,
	}, nil
}

func (w *SegmentStoreWriter) SegmentCount() uint64 {
	fi, _ := w.idxFile.Stat()
	return uint64(fi.Size()) / segIdxEntrySize
}

// WriteSegment writes dat + ri data as a single frame in the cdat file.
// recSplitPath is the temp file produced by RecSplit Build(), read into
// memory then deleted. Pass "" for batch-only data (no RecSplit).
func (w *SegmentStoreWriter) WriteSegment(datBytes []byte, recSplitPath string) (uint64, error) {
	segNum := w.SegmentCount()

	var riBytes []byte
	if recSplitPath != "" {
		var err error
		riBytes, err = os.ReadFile(recSplitPath)
		if err != nil {
			return 0, fmt.Errorf("read recsplit %s: %w", recSplitPath, err)
		}
		os.Remove(recSplitPath)
		os.Remove(recSplitPath + ".tmp")
	}

	frameSize := int64(4 + len(datBytes))
	if len(riBytes) > 0 {
		frameSize += int64(4 + len(riBytes))
	}
	if w.headSize+frameSize > segStoreMaxFileSize {
		w.headFile++
		w.headSize = 0
	}

	cdatPath := filepath.Join(w.dir, fmt.Sprintf("%s.%04d.cdat", w.prefix, w.headFile))
	cdatFile, err := os.OpenFile(cdatPath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return 0, err
	}
	cdatFile.Seek(0, 2)

	datOffset := w.headSize
	var riOffset int64

	var sizeBuf [4]byte
	binary.LittleEndian.PutUint32(sizeBuf[:], uint32(len(datBytes)))
	cdatFile.Write(sizeBuf[:])
	cdatFile.Write(datBytes)

	if len(riBytes) > 0 {
		riOffset = datOffset + 4 + int64(len(datBytes))
		binary.LittleEndian.PutUint32(sizeBuf[:], uint32(len(riBytes)))
		cdatFile.Write(sizeBuf[:])
		cdatFile.Write(riBytes)
	}

	cdatFile.Sync()
	cdatFile.Close()

	var idxEntry [segIdxEntrySize]byte
	binary.LittleEndian.PutUint16(idxEntry[0:2], w.headFile)
	binary.LittleEndian.PutUint32(idxEntry[4:8], uint32(datOffset))
	binary.LittleEndian.PutUint32(idxEntry[8:12], uint32(riOffset))
	w.idxFile.Write(idxEntry[:])
	w.idxFile.Sync()

	w.headSize += frameSize
	return segNum, nil
}

func (w *SegmentStoreWriter) Close() {
	if w.idxFile != nil {
		w.idxFile.Close()
	}
}

// segMmap holds a read-only memory-map of a .cdat data file so each segment's
// embedded RecSplit index can be referenced IN PLACE (off-heap) instead of
// being read into a heap buffer per lookup. The OS pages it on demand and may
// evict under memory pressure, so a cold/missing tx-hash lookup that fans out
// across many segments no longer pulls every segment's multi-MB index into the
// Go heap (the old OpenIndexFromBytes(make([]byte,riSize)) path could resident
// the entire txindex — 12+ GB — on a single miss).
type segMmap struct {
	data   []byte
	handle *[mmap.MaxMapSize]byte
}

// SegmentStoreReader reads chain archive segments.
type SegmentStoreReader struct {
	dir       string
	prefix    string
	idxFile   *os.File
	segments  uint64
	dataFiles map[uint16]*os.File
	mmaps     map[uint16]*segMmap
	riCache   map[uint64]*recsplit.Index
}

// getDataMmap lazily memory-maps the .cdat data file for fileNum (read-only)
// and caches the mapping. The returned slice is the whole file; callers slice
// the embedded RecSplit-index region out of it without copying.
func (r *SegmentStoreReader) getDataMmap(fileNum uint16) ([]byte, error) {
	if m, ok := r.mmaps[fileNum]; ok {
		return m.data, nil
	}
	df, err := r.getDataFile(fileNum)
	if err != nil {
		return nil, err
	}
	fi, err := df.Stat()
	if err != nil {
		return nil, err
	}
	data, handle, err := mmap.Mmap(df, int(fi.Size()))
	if err != nil {
		return nil, fmt.Errorf("mmap data file %d: %w", fileNum, err)
	}
	if r.mmaps == nil {
		r.mmaps = make(map[uint16]*segMmap)
	}
	r.mmaps[fileNum] = &segMmap{data: data, handle: handle}
	return data, nil
}

// OpenSegmentStore opens a segment store for reading.
func OpenSegmentStore(dir, prefix string) (*SegmentStoreReader, error) {
	idxPath := filepath.Join(dir, prefix+".cidx")
	idxFile, err := os.Open(idxPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Try legacy path: {dir}/{prefix}/segments.idx
			return openLegacyStore(dir, prefix)
		}
		return nil, err
	}
	fi, _ := idxFile.Stat()
	segments := uint64(fi.Size()) / segIdxEntrySize

	return &SegmentStoreReader{
		dir: dir, prefix: prefix, idxFile: idxFile,
		segments:  segments,
		dataFiles: make(map[uint16]*os.File),
		riCache:   make(map[uint64]*recsplit.Index),
	}, nil
}

// openLegacyStore tries the old directory-per-type layout.
func openLegacyStore(dir, prefix string) (*SegmentStoreReader, error) {
	legacyDir := filepath.Join(dir, prefix)
	legacyIdx := filepath.Join(legacyDir, "segments.idx")
	idxFile, err := os.Open(legacyIdx)
	if err != nil {
		if os.IsNotExist(err) {
			return &SegmentStoreReader{
				dir: dir, prefix: prefix,
				dataFiles: make(map[uint16]*os.File),
				riCache:   make(map[uint64]*recsplit.Index),
			}, nil
		}
		return nil, err
	}
	fi, _ := idxFile.Stat()
	entrySize := int64(segIdxEntrySize)
	segments := uint64(fi.Size()) / uint64(entrySize)
	if segments == 0 && fi.Size() >= 8 {
		entrySize = 8
		segments = uint64(fi.Size()) / uint64(entrySize)
	}

	return &SegmentStoreReader{
		dir: legacyDir, prefix: "data", // legacy uses data.NNNN.dat
		idxFile:   idxFile,
		segments:  segments,
		dataFiles: make(map[uint16]*os.File),
		riCache:   make(map[uint64]*recsplit.Index),
	}, nil
}

func (r *SegmentStoreReader) SegmentCount() uint64 { return r.segments }

func (r *SegmentStoreReader) readIdxEntry(segNum uint64) (fileNum uint16, datOff, riOff uint32, err error) {
	if segNum >= r.segments {
		return 0, 0, 0, fmt.Errorf("segment %d out of range (%d)", segNum, r.segments)
	}

	fi, _ := r.idxFile.Stat()
	entrySize := int64(segIdxEntrySize)
	if fi.Size()/int64(r.segments) == 8 {
		entrySize = 8
	}

	var buf [segIdxEntrySize]byte
	if _, err := r.idxFile.ReadAt(buf[:entrySize], int64(segNum)*entrySize); err != nil {
		return 0, 0, 0, err
	}
	fileNum = binary.LittleEndian.Uint16(buf[0:2])
	datOff = binary.LittleEndian.Uint32(buf[4:8])
	if entrySize >= segIdxEntrySize {
		riOff = binary.LittleEndian.Uint32(buf[8:12])
	}
	return fileNum, datOff, riOff, nil
}

func (r *SegmentStoreReader) getDataFile(fileNum uint16) (*os.File, error) {
	df, ok := r.dataFiles[fileNum]
	if ok {
		return df, nil
	}
	// Try new naming: {prefix}.NNNN.cdat
	path := filepath.Join(r.dir, fmt.Sprintf("%s.%04d.cdat", r.prefix, fileNum))
	f, err := os.Open(path)
	if err != nil {
		// Fallback: legacy data.NNNN.dat
		path = filepath.Join(r.dir, fmt.Sprintf("data.%04d.dat", fileNum))
		f, err = os.Open(path)
		if err != nil {
			return nil, err
		}
	}
	r.dataFiles[fileNum] = f
	return f, nil
}

func (r *SegmentStoreReader) ReadSegmentData(segNum uint64) ([]byte, error) {
	fileNum, datOff, _, err := r.readIdxEntry(segNum)
	if err != nil {
		return nil, err
	}
	df, err := r.getDataFile(fileNum)
	if err != nil {
		return nil, err
	}

	var sizeBuf [4]byte
	if _, err := df.ReadAt(sizeBuf[:], int64(datOff)); err != nil {
		return nil, err
	}
	dataSize := binary.LittleEndian.Uint32(sizeBuf[:])
	data := make([]byte, dataSize)
	if _, err := df.ReadAt(data, int64(datOff)+4); err != nil {
		return nil, err
	}
	return data, nil
}

func (r *SegmentStoreReader) GetRecSplitReader(segNum uint64) (*recsplit.IndexReader, error) {
	idx, err := r.GetRecSplitIndex(segNum)
	if err != nil {
		return nil, err
	}
	return recsplit.NewIndexReader(idx), nil
}

func (r *SegmentStoreReader) GetRecSplitIndex(segNum uint64) (*recsplit.Index, error) {
	if idx, ok := r.riCache[segNum]; ok {
		return idx, nil
	}

	fileNum, _, riOff, err := r.readIdxEntry(segNum)
	if err != nil {
		return nil, err
	}

	// Legacy format: separate .ri files
	fi, _ := r.idxFile.Stat()
	if fi.Size()/int64(r.segments) == 8 {
		riPath := filepath.Join(r.dir, fmt.Sprintf("%06d.ri", segNum))
		idx, err := recsplit.OpenIndex(riPath)
		if err != nil {
			return nil, err
		}
		r.riCache[segNum] = idx
		return idx, nil
	}

	if riOff == 0 {
		return nil, fmt.Errorf("segment %d has no embedded RecSplit", segNum)
	}

	// mmap the data file and reference the embedded ri region in place — no
	// heap copy. The OS pages only the touched index bytes; this is what keeps
	// a fan-out lookup from residenting every segment's index in the Go heap.
	mm, err := r.getDataMmap(fileNum)
	if err != nil {
		return nil, err
	}
	if int64(riOff)+4 > int64(len(mm)) {
		return nil, fmt.Errorf("ri size offset %d beyond data file len %d", riOff, len(mm))
	}
	riSize := binary.LittleEndian.Uint32(mm[riOff : riOff+4])
	start := int64(riOff) + 4
	end := start + int64(riSize)
	if end > int64(len(mm)) {
		return nil, fmt.Errorf("ri data [%d,%d) beyond data file len %d", start, end, len(mm))
	}
	// Reference the index bytes in place inside the mmap (cap==len so the
	// reader can't read past). The RecSplit reader casts an internal 8-aligned
	// sub-region to []uint64 via unsafe; on amd64 (our target) an unaligned
	// 8-byte load is tolerated, and the ② rebuild pads each segment's ri to an
	// 8-byte file offset so this is also alignment-correct for portable builds.
	riData := mm[start:end:end]

	idx, err := recsplit.OpenIndexFromBytes(riData, fmt.Sprintf("seg%06d", segNum))
	if err != nil {
		return nil, fmt.Errorf("parse recsplit for segment %d: %w", segNum, err)
	}
	r.riCache[segNum] = idx
	return idx, nil
}

func (r *SegmentStoreReader) Close() {
	if r.idxFile != nil {
		r.idxFile.Close()
	}
	// Close indexes first (their data may slice into the mmaps below), then
	// unmap the data files, then close the underlying file handles.
	for _, idx := range r.riCache {
		idx.Close()
	}
	for _, m := range r.mmaps {
		_ = mmap.Munmap(m.data, m.handle)
	}
	for _, f := range r.dataFiles {
		f.Close()
	}
}

func renameWithRetry(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	for i := 0; i < 5; i++ {
		time.Sleep(100 * time.Millisecond)
		if err = os.Rename(src, dst); err == nil {
			return nil
		}
	}
	return err
}
