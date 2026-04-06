// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// history_segment.go — read-only RecSplit-indexed history lookup.
// Maps key(addr or addr+slot) → sorted block numbers where key changed.
// 96.9% of storage keys are single-write → 1 varint blockNum.

package cscompact

import (
	"encoding/binary"
	"fmt"
	"os"
	"sort"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/lib/recsplit"
)

const (
	HistSegmentSize = 1_000_000 // blocks per history segment
	histDatMagic    = "HV01"
)

// HistorySegment is a read-only RecSplit-indexed history segment.
type HistorySegment struct {
	startBlock uint64
	endBlock   uint64
	idx        *recsplit.Index
	reader     *recsplit.IndexReader
	dat        []byte   // mmap'd or loaded
	datFile    *os.File
	keyCount   uint64
}

// OpenHistorySegment opens idx + dat files for a history segment.
func OpenHistorySegment(idxPath, datPath string) (*HistorySegment, error) {
	idx, err := recsplit.OpenIndex(idxPath)
	if err != nil {
		return nil, fmt.Errorf("open idx: %w", err)
	}

	datFile, err := os.Open(datPath)
	if err != nil {
		idx.Close()
		return nil, fmt.Errorf("open dat: %w", err)
	}

	fi, err := datFile.Stat()
	if err != nil {
		datFile.Close()
		idx.Close()
		return nil, err
	}

	dat := make([]byte, fi.Size())
	if fi.Size() > 0 {
		if _, err := datFile.ReadAt(dat, 0); err != nil {
			datFile.Close()
			idx.Close()
			return nil, fmt.Errorf("read dat: %w", err)
		}
	}

	// Try zstd decompress (dat may be compressed).
	if len(dat) > 4 && string(dat[:4]) != histDatMagic {
		dec, err := zstd.NewReader(nil)
		if err == nil {
			if decompressed, err := dec.DecodeAll(dat, nil); err == nil && len(decompressed) >= 12 && string(decompressed[:4]) == histDatMagic {
				dat = decompressed
			}
			dec.Close()
		}
	}

	// Parse header.
	if len(dat) < 12 || string(dat[:4]) != histDatMagic {
		datFile.Close()
		idx.Close()
		return nil, fmt.Errorf("invalid dat magic")
	}
	keyCount := binary.LittleEndian.Uint32(dat[4:8])
	// flags := binary.LittleEndian.Uint32(dat[8:12]) // reserved

	if uint64(keyCount) != idx.KeyCount() {
		datFile.Close()
		idx.Close()
		return nil, fmt.Errorf("keyCount mismatch: dat=%d idx=%d", keyCount, idx.KeyCount())
	}

	startBlock := idx.BaseDataID()

	return &HistorySegment{
		startBlock: startBlock,
		endBlock:   startBlock + HistSegmentSize,
		idx:        idx,
		reader:     recsplit.NewIndexReader(idx),
		dat:        dat,
		datFile:    datFile,
		keyCount:   uint64(keyCount),
	}, nil
}

// Lookup finds the largest block number <= blockNum where key changed.
// Returns (foundBlock, true) or (0, false).
func (s *HistorySegment) Lookup(key []byte, blockNum uint64) (uint64, bool) {
	if s.idx.Empty() {
		return 0, false
	}

	ordinal, found := s.reader.Lookup(key)
	if !found {
		return 0, false
	}
	if ordinal >= s.keyCount {
		return 0, false
	}

	// Read adaptive value at ordinal's offset.
	// Offset table: after 12B header, offsets[keyCount] × 4B LE.
	offsetTableStart := uint64(12)
	offsetPos := offsetTableStart + ordinal*4
	if offsetPos+4 > uint64(len(s.dat)) {
		return 0, false
	}
	dataOffset := binary.LittleEndian.Uint32(s.dat[offsetPos : offsetPos+4])
	pos := int(dataOffset)

	if pos >= len(s.dat) {
		return 0, false
	}

	// Read writeCount.
	count := int(s.dat[pos])
	pos++
	if count == 0xFF {
		if pos+4 > len(s.dat) {
			return 0, false
		}
		count = int(binary.LittleEndian.Uint32(s.dat[pos:]))
		pos += 4
	}

	if count == 0 {
		return 0, false
	}

	// Read block numbers (delta-encoded).
	blocks := make([]uint64, 0, count)
	prev := uint64(0)
	for i := 0; i < count; i++ {
		delta, n := binary.Uvarint(s.dat[pos:])
		if n <= 0 {
			break
		}
		pos += n
		prev += delta
		blocks = append(blocks, prev)
	}

	// Find largest block <= blockNum.
	idx := sort.Search(len(blocks), func(i int) bool {
		return blocks[i] > blockNum
	}) - 1

	if idx < 0 {
		return 0, false
	}
	return blocks[idx], true
}

func (s *HistorySegment) KeyCount() uint64    { return s.keyCount }
func (s *HistorySegment) StartBlock() uint64  { return s.startBlock }

func (s *HistorySegment) Close() {
	if s.idx != nil {
		s.idx.Close()
	}
	if s.datFile != nil {
		s.datFile.Close()
	}
}

// HistSegmentFileName returns base name for a history segment.
func HistSegmentFileName(prefix string, startBlock, endBlock uint64) string {
	return fmt.Sprintf("%s.%06d-%06d", prefix, startBlock/1000, endBlock/1000)
}

// HistoryReader provides tiered history lookup using SegmentStoreReader.
type HistoryReader struct {
	store          *SegmentStoreReader
	segmentSize    uint64
	cachedSegNum   int64
	cachedSegment  *HistorySegment
}

func NewHistoryReader(dir, prefix string) (*HistoryReader, error) {
	store, err := OpenSegmentStore(dir, prefix)
	if err != nil {
		return nil, err
	}
	return &HistoryReader{
		store:        store,
		segmentSize:  HistSegmentSize,
		cachedSegNum: -1,
	}, nil
}

func (r *HistoryReader) Lookup(key []byte, blockNum uint64) (uint64, bool) {
	segNum := int64(blockNum / r.segmentSize)
	if uint64(segNum) >= r.store.SegmentCount() {
		return 0, false
	}

	if segNum != r.cachedSegNum {
		if err := r.loadSegment(segNum); err != nil {
			return 0, false
		}
	}
	if r.cachedSegment == nil {
		return 0, false
	}
	return r.cachedSegment.Lookup(key, blockNum)
}

func (r *HistoryReader) loadSegment(segNum int64) error {
	data, err := r.store.ReadSegmentData(uint64(segNum))
	if err != nil {
		return err
	}

	// zstd decompress if needed.
	if len(data) > 4 && string(data[:4]) != histDatMagic {
		dec, err := zstd.NewReader(nil)
		if err == nil {
			if decompressed, err := dec.DecodeAll(data, nil); err == nil {
				data = decompressed
			}
			dec.Close()
		}
	}

	if len(data) < 12 || string(data[:4]) != histDatMagic {
		return fmt.Errorf("invalid dat for segment %d", segNum)
	}

	keyCount := binary.LittleEndian.Uint32(data[4:8])

	reader, err := r.store.GetRecSplitReader(uint64(segNum))
	if err != nil {
		return err
	}
	idx, _ := r.store.GetRecSplitIndex(uint64(segNum))

	r.cachedSegNum = segNum
	r.cachedSegment = &HistorySegment{
		startBlock: uint64(segNum) * r.segmentSize,
		endBlock:   uint64(segNum+1) * r.segmentSize,
		idx:        idx,
		reader:     reader,
		dat:        data,
		keyCount:   uint64(keyCount),
	}
	return nil
}

func (r *HistoryReader) Close() {
	r.store.Close()
}
