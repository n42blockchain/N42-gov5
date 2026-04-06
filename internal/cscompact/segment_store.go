// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// segment_store.go — freezer-style file management for RecSplit segments.
//
// Unified file layout for txlookup and history:
//
//	{dir}/
//	├── segments.idx         # master index: [8B per segment]
//	├── data.0000.dat        # framed value data ≤2GB
//	├── data.0001.dat        # rotation
//	├── 000000.ri            # RecSplit index per segment
//	├── 000001.ri
//	└── ...
//
// segments.idx entry: [2B fileNum LE][2B reserved][4B offset LE]
// data.NNNN.dat frame: [4B size LE][data bytes]

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

const segStoreMaxFileSize = 2_000_000_000 // 2 GB

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

	// Resume: determine head file from last idx entry.
	fi, _ := idxFile.Stat()
	existingSegs := fi.Size() / 8
	var headFile uint16
	var headSize int64
	if existingSegs > 0 {
		var lastEntry [8]byte
		idxFile.ReadAt(lastEntry[:], (existingSegs-1)*8)
		headFile = binary.LittleEndian.Uint16(lastEntry[0:2])
		datPath := filepath.Join(dir, fmt.Sprintf("data.%04d.dat", headFile))
		if dfi, err := os.Stat(datPath); err == nil {
			headSize = dfi.Size()
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
	return uint64(fi.Size()) / 8
}

// WriteSegment writes a data segment and its RecSplit index.
// Returns the segment number assigned.
func (w *SegmentStoreWriter) WriteSegment(data []byte, recSplitPath string) (uint64, error) {
	segNum := w.SegmentCount()

	// Rotate dat file if needed.
	frameSize := int64(4 + len(data))
	if w.headSize+frameSize > segStoreMaxFileSize {
		w.headFile++
		w.headSize = 0
	}

	// Write data frame.
	datPath := filepath.Join(w.dir, fmt.Sprintf("data.%04d.dat", w.headFile))
	datFile, err := os.OpenFile(datPath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return 0, err
	}
	datFile.Seek(0, 2)
	var sizeBuf [4]byte
	binary.LittleEndian.PutUint32(sizeBuf[:], uint32(len(data)))
	datFile.Write(sizeBuf[:])
	datFile.Write(data)
	datFile.Sync()
	datFile.Close()

	// Write idx entry.
	var idxEntry [8]byte
	binary.LittleEndian.PutUint16(idxEntry[0:2], w.headFile)
	binary.LittleEndian.PutUint32(idxEntry[4:8], uint32(w.headSize))
	w.idxFile.Write(idxEntry[:])
	w.idxFile.Sync()

	// Rename RecSplit to sequential number.
	riPath := filepath.Join(w.dir, fmt.Sprintf("%06d.ri", segNum))
	if recSplitPath != riPath {
		if err := renameWithRetry(recSplitPath, riPath); err != nil {
			return 0, fmt.Errorf("rename recsplit %s → %s: %w", recSplitPath, riPath, err)
		}
	}

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
	riFiles   map[uint64]*recsplit.Index
	riReaders map[uint64]*recsplit.IndexReader
}

func OpenSegmentStore(dir string) (*SegmentStoreReader, error) {
	idxPath := filepath.Join(dir, "segments.idx")
	idxFile, err := os.Open(idxPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &SegmentStoreReader{
				dir: dir, dataFiles: make(map[uint16]*os.File),
				riFiles: make(map[uint64]*recsplit.Index),
				riReaders: make(map[uint64]*recsplit.IndexReader),
			}, nil
		}
		return nil, err
	}
	fi, _ := idxFile.Stat()

	return &SegmentStoreReader{
		dir:       dir,
		idxFile:   idxFile,
		segments:  uint64(fi.Size()) / 8,
		dataFiles: make(map[uint16]*os.File),
		riFiles:   make(map[uint64]*recsplit.Index),
		riReaders: make(map[uint64]*recsplit.IndexReader),
	}, nil
}

func (r *SegmentStoreReader) SegmentCount() uint64 { return r.segments }

// ReadSegmentData reads the data frame for a segment.
func (r *SegmentStoreReader) ReadSegmentData(segNum uint64) ([]byte, error) {
	if segNum >= r.segments {
		return nil, fmt.Errorf("segment %d out of range (%d)", segNum, r.segments)
	}

	var entryBuf [8]byte
	if _, err := r.idxFile.ReadAt(entryBuf[:], int64(segNum)*8); err != nil {
		return nil, err
	}
	fileNum := binary.LittleEndian.Uint16(entryBuf[0:2])
	offset := binary.LittleEndian.Uint32(entryBuf[4:8])

	df, ok := r.dataFiles[fileNum]
	if !ok {
		path := filepath.Join(r.dir, fmt.Sprintf("data.%04d.dat", fileNum))
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		r.dataFiles[fileNum] = f
		df = f
	}

	var sizeBuf [4]byte
	if _, err := df.ReadAt(sizeBuf[:], int64(offset)); err != nil {
		return nil, err
	}
	dataSize := binary.LittleEndian.Uint32(sizeBuf[:])

	data := make([]byte, dataSize)
	if _, err := df.ReadAt(data, int64(offset)+4); err != nil {
		return nil, err
	}
	return data, nil
}

// GetRecSplitReader returns a cached RecSplit reader for a segment.
func (r *SegmentStoreReader) GetRecSplitReader(segNum uint64) (*recsplit.IndexReader, error) {
	if reader, ok := r.riReaders[segNum]; ok {
		return reader, nil
	}

	riPath := filepath.Join(r.dir, fmt.Sprintf("%06d.ri", segNum))
	idx, err := recsplit.OpenIndex(riPath)
	if err != nil {
		return nil, err
	}
	reader := recsplit.NewIndexReader(idx)
	r.riFiles[segNum] = idx
	r.riReaders[segNum] = reader
	return reader, nil
}

// GetRecSplitIndex returns the raw RecSplit index for a segment.
func (r *SegmentStoreReader) GetRecSplitIndex(segNum uint64) (*recsplit.Index, error) {
	if idx, ok := r.riFiles[segNum]; ok {
		return idx, nil
	}
	riPath := filepath.Join(r.dir, fmt.Sprintf("%06d.ri", segNum))
	idx, err := recsplit.OpenIndex(riPath)
	if err != nil {
		return nil, err
	}
	r.riFiles[segNum] = idx
	r.riReaders[segNum] = recsplit.NewIndexReader(idx)
	return idx, nil
}

func (r *SegmentStoreReader) Close() {
	if r.idxFile != nil {
		r.idxFile.Close()
	}
	for _, f := range r.dataFiles {
		f.Close()
	}
	for _, idx := range r.riFiles {
		idx.Close()
	}
}

// renameWithRetry handles Windows file locking delays (antivirus, indexer).
// On non-Windows platforms, this is a single os.Rename call.
func renameWithRetry(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	// Windows: retry up to 5 times with short backoff.
	for i := 0; i < 5; i++ {
		time.Sleep(100 * time.Millisecond)
		if err = os.Rename(src, dst); err == nil {
			return nil
		}
	}
	return err
}
