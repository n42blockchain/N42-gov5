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
	segments uint64
	poisoned bool
	// fault is set only by package tests to interrupt durability boundaries.
	fault func(string) error
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

	fi, err := idxFile.Stat()
	if err != nil {
		idxFile.Close()
		return nil, err
	}
	if fi.Size()%segIdxEntrySize != 0 {
		idxFile.Close()
		return nil, fmt.Errorf("%s: incomplete segment index entry", idxPath)
	}
	existingSegs := fi.Size() / segIdxEntrySize
	var headFile uint16
	var headSize int64
	if existingSegs > 0 {
		var lastEntry [segIdxEntrySize]byte
		if _, err := idxFile.ReadAt(lastEntry[:], (existingSegs-1)*segIdxEntrySize); err != nil {
			idxFile.Close()
			return nil, err
		}
		headFile = binary.LittleEndian.Uint16(lastEntry[0:2])
		cdatPath := filepath.Join(dir, fmt.Sprintf("%s.%04d.cdat", prefix, headFile))
		dfi, err := os.Stat(cdatPath)
		if err != nil {
			idxFile.Close()
			return nil, err
		}
		headSize = dfi.Size()
	} else {
		// A crash may leave an orphan data frame before the first cidx
		// publication. The next frame must never point into that orphan.
		if dfi, err := os.Stat(filepath.Join(dir, fmt.Sprintf("%s.%04d.cdat", prefix, headFile))); err == nil {
			headSize = dfi.Size()
		} else if !os.IsNotExist(err) {
			idxFile.Close()
			return nil, err
		}
	}
	if _, err := idxFile.Seek(0, 2); err != nil {
		idxFile.Close()
		return nil, err
	}

	return &SegmentStoreWriter{
		dir: dir, prefix: prefix, idxFile: idxFile,
		headFile: headFile, headSize: headSize, segments: uint64(existingSegs),
	}, nil
}

func (w *SegmentStoreWriter) SegmentCount() uint64 {
	return w.segments
}

// WriteSegment writes dat + ri data as a single frame in the cdat file.
// recSplitPath is the temp file produced by RecSplit Build(), read into
// memory then deleted. Pass "" for batch-only data (no RecSplit).
func (w *SegmentStoreWriter) WriteSegment(datBytes []byte, recSplitPath string) (uint64, error) {
	if w.poisoned {
		return 0, fmt.Errorf("segment writer poisoned after failed publication")
	}
	segNum := w.SegmentCount()

	var riBytes []byte
	if recSplitPath != "" {
		var err error
		riBytes, err = os.ReadFile(recSplitPath)
		if err != nil {
			return 0, fmt.Errorf("read recsplit %s: %w", recSplitPath, err)
		}
		// Keep the temporary index until the frame is fully published. A
		// failed write must not consume its input or invite a blind retry.
	}

	frameSize := int64(4 + len(datBytes))
	if len(riBytes) > 0 {
		frameSize += int64(4 + len(riBytes))
	}
	if frameSize > segStoreMaxFileSize {
		w.poisoned = true
		return 0, fmt.Errorf("segment frame exceeds maximum data file size")
	}
	var cdatFile *os.File
	var cdatPath string
	var newDataFile bool
	var datOffset int64
	var err error
	for {
		cdatPath = filepath.Join(w.dir, fmt.Sprintf("%s.%04d.cdat", w.prefix, w.headFile))
		_, statErr := os.Stat(cdatPath)
		newDataFile = os.IsNotExist(statErr)
		if statErr != nil && !newDataFile {
			w.poisoned = true
			return 0, statErr
		}
		cdatFile, err = os.OpenFile(cdatPath, os.O_RDWR|os.O_CREATE, 0644)
		if err != nil {
			w.poisoned = true
			return 0, err
		}
		if datOffset, err = cdatFile.Seek(0, 2); err != nil {
			cdatFile.Close()
			w.poisoned = true
			return 0, err
		}
		if datOffset+frameSize <= segStoreMaxFileSize {
			break
		}
		if err := cdatFile.Close(); err != nil {
			w.poisoned = true
			return 0, err
		}
		if w.headFile == ^uint16(0) {
			w.poisoned = true
			return 0, fmt.Errorf("segment data file number exhausted")
		}
		w.headFile++
	}
	var riOffset int64

	var sizeBuf [4]byte
	binary.LittleEndian.PutUint32(sizeBuf[:], uint32(len(datBytes)))
	if err = writeAll(cdatFile, sizeBuf[:]); err == nil {
		err = writeAll(cdatFile, datBytes)
	}

	if err == nil && len(riBytes) > 0 {
		riOffset = datOffset + 4 + int64(len(datBytes))
		binary.LittleEndian.PutUint32(sizeBuf[:], uint32(len(riBytes)))
		if err = writeAll(cdatFile, sizeBuf[:]); err == nil {
			err = writeAll(cdatFile, riBytes)
		}
	}
	if err == nil {
		err = cdatFile.Sync()
	}
	if closeErr := cdatFile.Close(); err == nil {
		err = closeErr
	}
	if err == nil && newDataFile {
		err = syncSegmentDirectory(w.dir)
	}
	if err == nil && w.fault != nil {
		err = w.fault("data-durable")
	}
	if err != nil {
		w.poisoned = true
		return 0, fmt.Errorf("write segment data: %w", err)
	}

	var idxEntry [segIdxEntrySize]byte
	binary.LittleEndian.PutUint16(idxEntry[0:2], w.headFile)
	binary.LittleEndian.PutUint32(idxEntry[4:8], uint32(datOffset))
	binary.LittleEndian.PutUint32(idxEntry[8:12], uint32(riOffset))
	// Publish one complete cidx generation. A reader may hold the old inode;
	// after rename, a new reader sees the entire new entry or the old file.
	if err := w.publishIndex(idxEntry[:]); err != nil {
		w.poisoned = true
		return 0, err
	}
	if recSplitPath != "" {
		os.Remove(recSplitPath)
		os.Remove(recSplitPath + ".tmp")
	}

	w.headSize = datOffset + frameSize
	w.segments++
	return segNum, nil
}

func writeAll(f *os.File, p []byte) error {
	for len(p) > 0 {
		n, err := f.Write(p)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("short segment write")
		}
		p = p[n:]
	}
	return nil
}

func syncSegmentDirectory(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = f.Sync()
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func (w *SegmentStoreWriter) publishIndex(entry []byte) error {
	path := filepath.Join(w.dir, w.prefix+".cidx")
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(current)%segIdxEntrySize != 0 {
		return fmt.Errorf("%s: incomplete segment index entry", path)
	}
	if uint64(len(current)/segIdxEntrySize) != w.segments {
		return fmt.Errorf("%s: segment index changed during publication", path)
	}
	tmp, err := os.CreateTemp(w.dir, w.prefix+".cidx-next-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = writeAll(tmp, current); err == nil {
		err = writeAll(tmp, entry)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if w.fault != nil {
		if err := w.fault("index-temp-durable"); err != nil {
			return err
		}
	}
	if err := renameWithRetry(tmpPath, path); err != nil {
		return err
	}
	if w.fault != nil {
		if err := w.fault("index-renamed"); err != nil {
			return err
		}
	}
	if err := syncSegmentDirectory(w.dir); err != nil {
		return err
	}
	next, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	old := w.idxFile
	w.idxFile = next
	return old.Close()
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
	fi, err := idxFile.Stat()
	if err != nil {
		idxFile.Close()
		return nil, err
	}
	if fi.Size()%segIdxEntrySize != 0 {
		idxFile.Close()
		return nil, fmt.Errorf("%s: incomplete segment index entry", idxPath)
	}
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
	fi, err := df.Stat()
	if err != nil {
		return nil, err
	}
	if int64(datOff)+4 > fi.Size() {
		return nil, fmt.Errorf("segment %d data offset beyond file", segNum)
	}

	var sizeBuf [4]byte
	if _, err := df.ReadAt(sizeBuf[:], int64(datOff)); err != nil {
		return nil, err
	}
	dataSize := binary.LittleEndian.Uint32(sizeBuf[:])
	if int64(dataSize) > fi.Size()-int64(datOff)-4 {
		return nil, fmt.Errorf("segment %d data frame truncated", segNum)
	}
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

	_, _, riOff, err := r.readIdxEntry(segNum)
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

	riData, err := r.ReadRecSplitBytes(segNum)
	if err != nil {
		return nil, err
	}
	idx, err := recsplit.OpenIndexFromBytes(riData, fmt.Sprintf("seg%06d", segNum))
	if err != nil {
		return nil, fmt.Errorf("parse recsplit for segment %d: %w", segNum, err)
	}
	r.riCache[segNum] = idx
	return idx, nil
}

// ReadRecSplitBytes exposes the exact embedded frame bytes for integrity
// checks before parsing. The returned slice is backed by this reader's mmap
// and is valid until Close; callers must not modify it.
func (r *SegmentStoreReader) ReadRecSplitBytes(segNum uint64) ([]byte, error) {
	fileNum, _, riOff, err := r.readIdxEntry(segNum)
	if err != nil {
		return nil, err
	}
	if riOff == 0 {
		return nil, fmt.Errorf("segment %d has no embedded RecSplit", segNum)
	}
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
	return riData, nil
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

// TruncateLastSegment removes the newest segment from the store: the cdat
// frame it occupies is cut back to its start offset, any later cdat file is
// deleted, and its cidx entry goes away. Used to rewrite a PARTIAL final
// segment (one whose source range ended mid-segment) on the next run —
// counting such a segment as full resumes at the next boundary and leaves a
// permanent hole, the same failure headerc/bodyc rewind against.
//
// Unlike the headerc rewind this also shrinks the cdat, so a weekly rebuild of
// the tail segment does not accumulate orphaned frames inside a published
// artefact.
func (w *SegmentStoreWriter) TruncateLastSegment() error {
	segs := w.SegmentCount()
	if segs == 0 {
		return fmt.Errorf("truncate: store is empty")
	}
	var lastEntry [segIdxEntrySize]byte
	if _, err := w.idxFile.ReadAt(lastEntry[:], int64(segs-1)*segIdxEntrySize); err != nil {
		return fmt.Errorf("truncate: read last idx entry: %w", err)
	}
	fileNum := binary.LittleEndian.Uint16(lastEntry[0:2])
	datOff := int64(binary.LittleEndian.Uint32(lastEntry[4:8]))

	// Drop whole cdat files newer than the one holding this segment. They can
	// only exist if a previous crash left them behind — no live entry points
	// into them once this entry is gone.
	for f := fileNum + 1; ; f++ {
		p := filepath.Join(w.dir, fmt.Sprintf("%s.%04d.cdat", w.prefix, f))
		if _, err := os.Stat(p); err != nil {
			break
		}
		if err := os.Remove(p); err != nil {
			return fmt.Errorf("truncate: remove %s: %w", p, err)
		}
	}

	cdatPath := filepath.Join(w.dir, fmt.Sprintf("%s.%04d.cdat", w.prefix, fileNum))
	if err := os.Truncate(cdatPath, datOff); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("truncate: cdat %s to %d: %w", cdatPath, datOff, err)
	}
	if err := w.idxFile.Truncate(int64(segs-1) * segIdxEntrySize); err != nil {
		return fmt.Errorf("truncate: cidx: %w", err)
	}
	if err := w.idxFile.Sync(); err != nil {
		return fmt.Errorf("truncate: sync cidx: %w", err)
	}
	if _, err := w.idxFile.Seek(0, 2); err != nil {
		return fmt.Errorf("truncate: seek cidx: %w", err)
	}
	w.headFile = fileNum
	w.headSize = datOff
	w.segments--
	return nil
}
