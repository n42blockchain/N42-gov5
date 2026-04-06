// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// segment_store.go — freezer-style file management for RecSplit segments.
//
// Unified file layout for txlookup and history:
//
//	{dir}/
//	├── segments.idx         # master index: [12B per segment]
//	├── data.0000.dat        # framed data: [datSize][dat][riSize][ri]
//	├── data.0001.dat        # rotation at 2GB
//	└── ...
//
// segments.idx entry: [2B fileNum LE][2B reserved][4B datOffset LE][4B riOffset LE]
// data.NNNN.dat frame: [4B datSize LE][datBytes][4B riSize LE][riBytes]

package cscompact

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/n42blockchain/N42/lib/recsplit"
)

const (
	segStoreMaxFileSize = 2_000_000_000 // 2 GB
	segIdxEntrySize     = 12            // fileNum(2) + reserved(2) + datOff(4) + riOff(4)
)

// SegmentStoreWriter writes segments in freezer style.
type SegmentStoreWriter struct {
	dir      string
	idxFile  *os.File
	headFile uint16
	headSize int64
}

func NewSegmentStoreWriter(dir string) (*SegmentStoreWriter, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	idxPath := filepath.Join(dir, "segments.idx")
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
		riOff := binary.LittleEndian.Uint32(lastEntry[8:12])
		// Estimate head size from riOffset + some overhead.
		datPath := filepath.Join(dir, fmt.Sprintf("data.%04d.dat", headFile))
		if dfi, err := os.Stat(datPath); err == nil {
			headSize = dfi.Size()
		} else {
			headSize = int64(riOff) + 4096 // fallback estimate
		}
	}
	idxFile.Seek(0, 2) // seek to end

	return &SegmentStoreWriter{
		dir: dir, idxFile: idxFile,
		headFile: headFile, headSize: headSize,
	}, nil
}

// SegmentCount returns the number of existing segments.
func (w *SegmentStoreWriter) SegmentCount() uint64 {
	fi, _ := w.idxFile.Stat()
	return uint64(fi.Size()) / segIdxEntrySize
}

// WriteSegment writes dat + ri data as a single frame in the data file.
// recSplitPath is the temp .ri file produced by RecSplit Build(), which
// is read into memory and then deleted.
func (w *SegmentStoreWriter) WriteSegment(datBytes []byte, recSplitPath string) (uint64, error) {
	segNum := w.SegmentCount()

	// Read RecSplit file into memory.
	riBytes, err := os.ReadFile(recSplitPath)
	if err != nil {
		return 0, fmt.Errorf("read recsplit %s: %w", recSplitPath, err)
	}
	os.Remove(recSplitPath)
	// Also remove .tmp variant that RecSplit may leave behind.
	os.Remove(recSplitPath + ".tmp")

	// Frame: [4B datSize][datBytes][4B riSize][riBytes]
	frameSize := int64(4 + len(datBytes) + 4 + len(riBytes))
	if w.headSize+frameSize > segStoreMaxFileSize {
		w.headFile++
		w.headSize = 0
	}

	datPath := filepath.Join(w.dir, fmt.Sprintf("data.%04d.dat", w.headFile))
	datFile, err := os.OpenFile(datPath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return 0, err
	}
	datFile.Seek(0, 2)

	datOffset := w.headSize
	riOffset := datOffset + 4 + int64(len(datBytes))

	// Write dat frame.
	var sizeBuf [4]byte
	binary.LittleEndian.PutUint32(sizeBuf[:], uint32(len(datBytes)))
	datFile.Write(sizeBuf[:])
	datFile.Write(datBytes)

	// Write ri frame.
	binary.LittleEndian.PutUint32(sizeBuf[:], uint32(len(riBytes)))
	datFile.Write(sizeBuf[:])
	datFile.Write(riBytes)

	datFile.Sync()
	datFile.Close()

	// Write idx entry: [fileNum(2)][reserved(2)][datOff(4)][riOff(4)]
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

// SegmentStoreReader reads segments in freezer style.
type SegmentStoreReader struct {
	dir       string
	idxFile   *os.File
	segments  uint64
	dataFiles map[uint16]*os.File

	// Cached RecSplit indices loaded from data frames (not separate files).
	riCache map[uint64]*recsplit.Index
}

func OpenSegmentStore(dir string) (*SegmentStoreReader, error) {
	idxPath := filepath.Join(dir, "segments.idx")
	idxFile, err := os.Open(idxPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &SegmentStoreReader{
				dir: dir, dataFiles: make(map[uint16]*os.File),
				riCache: make(map[uint64]*recsplit.Index),
			}, nil
		}
		return nil, err
	}
	fi, _ := idxFile.Stat()

	// Detect entry size: 12B (new) or 8B (legacy .ri files).
	entrySize := int64(segIdxEntrySize)
	segments := uint64(fi.Size()) / uint64(entrySize)
	if segments == 0 && fi.Size() >= 8 {
		// Could be legacy 8B entries.
		entrySize = 8
		segments = uint64(fi.Size()) / uint64(entrySize)
	}

	return &SegmentStoreReader{
		dir:       dir,
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

	// Detect entry size from file.
	fi, _ := r.idxFile.Stat()
	entrySize := int64(segIdxEntrySize)
	if fi.Size()/int64(r.segments) == 8 {
		entrySize = 8 // legacy format
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
	path := filepath.Join(r.dir, fmt.Sprintf("data.%04d.dat", fileNum))
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	r.dataFiles[fileNum] = f
	return f, nil
}

// ReadSegmentData reads the dat portion of a segment frame.
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

// GetRecSplitReader returns a cached RecSplit reader for a segment.
// The ri data is read from the embedded frame (not a separate .ri file).
func (r *SegmentStoreReader) GetRecSplitReader(segNum uint64) (*recsplit.IndexReader, error) {
	idx, err := r.GetRecSplitIndex(segNum)
	if err != nil {
		return nil, err
	}
	return recsplit.NewIndexReader(idx), nil
}

// GetRecSplitIndex returns the raw RecSplit index for a segment.
func (r *SegmentStoreReader) GetRecSplitIndex(segNum uint64) (*recsplit.Index, error) {
	if idx, ok := r.riCache[segNum]; ok {
		return idx, nil
	}

	fileNum, _, riOff, err := r.readIdxEntry(segNum)
	if err != nil {
		return nil, err
	}

	// If riOff == 0 and it's a legacy format, fall back to .ri files.
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

	df, err := r.getDataFile(fileNum)
	if err != nil {
		return nil, err
	}

	// Read ri size + data from frame.
	var sizeBuf [4]byte
	if _, err := df.ReadAt(sizeBuf[:], int64(riOff)); err != nil {
		return nil, fmt.Errorf("read ri size at offset %d: %w", riOff, err)
	}
	riSize := binary.LittleEndian.Uint32(sizeBuf[:])
	riData := make([]byte, riSize)
	if _, err := df.ReadAt(riData, int64(riOff)+4); err != nil {
		return nil, fmt.Errorf("read ri data (%d bytes at offset %d): %w", riSize, riOff+4, err)
	}

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
	for _, f := range r.dataFiles {
		f.Close()
	}
	for _, idx := range r.riCache {
		idx.Close()
	}
}

// renameWithRetry handles Windows file locking delays (antivirus, indexer).
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
