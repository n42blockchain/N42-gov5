// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// history_accum.go — inline history segment builder for the executor hot path.
// Accumulates changed keys across blocks, flushes to RecSplit segments on
// HistSegmentSize boundaries. Partial segments are written at shutdown.

package cscompact

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/klauspost/compress/zstd"

	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/recsplit"
	"github.com/n42blockchain/N42/log"
)

// HistoryAccumulator accumulates changed keys per block during execution,
// flushing to RecSplit segments when HistSegmentSize blocks are collected.
type HistoryAccumulator struct {
	dir    string
	prefix string
	store  *SegmentStoreWriter

	keyMap   map[string][]uint64 // key → block numbers
	segStart uint64              // first block of current accumulation window
}

func NewAccountHistoryAccumulator(outputDir string) (*HistoryAccumulator, error) {
	return newHistoryAccumulator(outputDir, "accthist")
}

func NewStorageHistoryAccumulator(outputDir string) (*HistoryAccumulator, error) {
	return newHistoryAccumulator(outputDir, "storhist")
}

func newHistoryAccumulator(outputDir, prefix string) (*HistoryAccumulator, error) {
	store, err := NewSegmentStoreWriter(outputDir, prefix)
	if err != nil {
		return nil, err
	}

	segStart := store.SegmentCount() * HistSegmentSize

	return &HistoryAccumulator{
		dir:      outputDir,
		prefix:   prefix,
		store:    store,
		keyMap:   make(map[string][]uint64),
		segStart: segStart,
	}, nil
}

// AddAccountKey records that an account address was modified at blockNum.
func (a *HistoryAccumulator) AddAccountKey(addr []byte, blockNum uint64) {
	if len(addr) < 20 {
		return
	}
	key := string(addr[:20])
	a.keyMap[key] = append(a.keyMap[key], blockNum)
}

// AddStorageKey records that a storage key (addr+slot) was modified at blockNum.
func (a *HistoryAccumulator) AddStorageKey(addr []byte, slot []byte, blockNum uint64) {
	if len(addr) < 20 || len(slot) < 32 {
		return
	}
	var compositeKey [52]byte
	copy(compositeKey[:20], addr[:20])
	copy(compositeKey[20:], slot[:32])
	key := string(compositeKey[:])
	a.keyMap[key] = append(a.keyMap[key], blockNum)
}

// AdvanceBlock checks if a full segment has been accumulated and flushes it.
func (a *HistoryAccumulator) AdvanceBlock(blockNum uint64) error {
	segEnd := a.segStart + HistSegmentSize
	if blockNum+1 >= segEnd {
		if err := a.flushSegment(a.segStart, segEnd); err != nil {
			return err
		}
		a.segStart = segEnd
	}
	return nil
}

// Flush writes any remaining accumulated data as a partial segment.
func (a *HistoryAccumulator) Flush(lastBlock uint64) error {
	if len(a.keyMap) == 0 {
		return nil
	}
	if lastBlock < a.segStart {
		return nil
	}
	return a.flushSegment(a.segStart, lastBlock+1)
}

func (a *HistoryAccumulator) flushSegment(startBlock, endBlock uint64) error {
	entries := sortAndDedup(a.keyMap)
	a.keyMap = make(map[string][]uint64)
	if len(entries) == 0 {
		return nil
	}

	log.Info("Flushing history segment",
		"prefix", a.prefix,
		"blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1),
		"keys", len(entries))

	tmpIdx := filepath.Join(a.dir, fmt.Sprintf("tmp_%s_%d.ri", a.prefix, startBlock))
	datBuf, err := buildHistSegment(entries, tmpIdx, startBlock, endBlock)
	if err != nil {
		os.Remove(tmpIdx)
		return err
	}

	segNum, err := a.store.WriteSegment(datBuf, tmpIdx)
	if err != nil {
		return err
	}

	log.Info("History segment written",
		"prefix", a.prefix,
		"segNum", segNum,
		"blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1),
		"keys", len(entries),
		"dat", fmt.Sprintf("%.1f KB", float64(len(datBuf))/1024))

	return nil
}

// Close flushes any remaining data and closes the segment store.
func (a *HistoryAccumulator) Close(lastBlock uint64) error {
	if err := a.Flush(lastBlock); err != nil {
		return err
	}
	a.store.Close()
	return nil
}

// SegmentCount returns the number of completed segments.
func (a *HistoryAccumulator) SegmentCount() uint64 {
	return a.store.SegmentCount()
}

// sortAndDedup collects keyMap entries into sorted, deduplicated histKeyData.
// Shared between HistoryAccumulator and HistoryBuilder.
func sortAndDedup(keyMap map[string][]uint64) []histKeyData {
	entries := make([]histKeyData, 0, len(keyMap))
	for mapKey, blocks := range keyMap {
		sort.Slice(blocks, func(i, j int) bool { return blocks[i] < blocks[j] })
		deduped := blocks[:0]
		for i, b := range blocks {
			if i == 0 || b != blocks[i-1] {
				deduped = append(deduped, b)
			}
		}
		entries = append(entries, histKeyData{key: []byte(mapKey), blocks: deduped})
	}
	sort.Slice(entries, func(i, j int) bool {
		return bytes.Compare(entries[i].key, entries[j].key) < 0
	})
	return entries
}

// buildHistSegment builds RecSplit index + zstd dat bytes for one segment.
// Shared between HistoryBuilder.buildSegment and HistoryAccumulator.
func buildHistSegment(entries []histKeyData, idxPath string, startBlock, endBlock uint64) ([]byte, error) {
	if len(entries) == 0 {
		logger := log2.New()
		rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
			KeyCount: 0, IndexFile: idxPath, TmpDir: os.TempDir(),
		}, logger)
		if err != nil {
			return nil, err
		}
		if err := rs.Build(context.Background()); err != nil {
			return nil, err
		}
		buf := make([]byte, 12)
		copy(buf[:4], histDatMagic)
		return buf, nil
	}

	logger := log2.New()
	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:           len(entries),
		BucketSize:         2000,
		LeafSize:           8,
		Enums:              false,
		LessFalsePositives: true,
		IndexFile:          idxPath,
		BaseDataID:         startBlock,
		TmpDir:             os.TempDir(),
	}, logger)
	if err != nil {
		return nil, err
	}
	for i, e := range entries {
		if err := rs.AddKey(e.key, uint64(i)); err != nil {
			return nil, err
		}
	}
	if err := rs.Build(context.Background()); err != nil {
		return nil, err
	}

	return buildHistDatBytes(entries)
}

// buildHistDatBytes creates the zstd-compressed dat content.
func buildHistDatBytes(entries []histKeyData) ([]byte, error) {
	keyCount := len(entries)
	headerSize := 12
	offsetTableSize := keyCount * 4
	dataStart := headerSize + offsetTableSize

	// Pre-allocate: ~10 bytes per key (count byte + avg 2 varints).
	dataBuf := make([]byte, 0, keyCount*10)
	offsets := make([]uint32, keyCount)

	for i, e := range entries {
		offsets[i] = uint32(dataStart + len(dataBuf))
		count := len(e.blocks)
		if count <= 254 {
			dataBuf = append(dataBuf, byte(count))
		} else {
			dataBuf = append(dataBuf, 0xFF)
			var cb [4]byte
			binary.LittleEndian.PutUint32(cb[:], uint32(count))
			dataBuf = append(dataBuf, cb[:]...)
		}
		prev := uint64(0)
		for _, bn := range e.blocks {
			dataBuf = appendVarint(dataBuf, bn-prev)
			prev = bn
		}
	}

	totalSize := dataStart + len(dataBuf)
	buf := make([]byte, totalSize)
	copy(buf[:4], histDatMagic)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(keyCount))
	for i, off := range offsets {
		binary.LittleEndian.PutUint32(buf[headerSize+i*4:], off)
	}
	copy(buf[dataStart:], dataBuf)

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return buf, nil
	}
	compressed := enc.EncodeAll(buf, nil)
	enc.Close()
	if len(compressed) < len(buf) {
		return compressed, nil
	}
	return buf, nil
}
