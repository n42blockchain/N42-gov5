// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// segment.go provides read-only access to a RecSplit-indexed tx hash segment.
// Each segment covers a range of blocks and maps txHash → blockNumber.
//
// .dat format v2 (Elias-Fano compressed):
//   Instead of 4 bytes per transaction (monotonically non-decreasing block numbers),
//   stores cumulative tx counts per block using Elias-Fano encoding.
//   This compresses 496 MB → ~1 MB for tx-dense segments (blocks 5M-6M).
//   Lookup: O(log blockCount) binary search on Elias-Fano vs O(1) array index.
//   Latency: ~200 ns vs ~50 ns — negligible for disk-backed lookups.

package txlookup

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"sort"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/recsplit"
	"github.com/n42blockchain/N42/lib/recsplit/eliasfano32"
)

// SegmentSize is the number of blocks per L1 segment.
const SegmentSize = 1_000_000

// datMagicV2 identifies Elias-Fano compressed .dat files.
// V1 (no magic) stored raw uint32 per tx; V2 uses Elias-Fano block boundaries.
var datMagicV2 = [4]byte{'E', 'F', 'D', '2'}

// TxSegment is a read-only RecSplit-indexed tx hash lookup segment.
type TxSegment struct {
	startBlock uint64
	endBlock   uint64
	idx        *recsplit.Index
	reader     *recsplit.IndexReader
	datFile    *os.File

	// V2: Elias-Fano block boundaries.
	ef         *eliasfano32.EliasFano
	blockCount uint64
	txCount    uint64

	// V1 fallback: raw uint32 array.
	dat []byte // nil when using V2
}

// OpenSegment opens a segment's .idx and .dat files.
// Auto-detects V1 (raw) vs V2 (Elias-Fano) format.
func OpenSegment(idxPath, datPath string) (*TxSegment, error) {
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

	startBlock := idx.BaseDataID()

	// Detect format: V2 starts with "EFD2" magic.
	if len(dat) >= 16 && bytes.Equal(dat[:4], datMagicV2[:]) {
		return openSegmentV2(idx, datFile, dat, startBlock)
	}
	return openSegmentV1(idx, datFile, dat, startBlock)
}

func openSegmentV1(idx *recsplit.Index, datFile *os.File, dat []byte, startBlock uint64) (*TxSegment, error) {
	txCount := idx.KeyCount()
	expectedDatSize := int64(txCount) * 4
	if int64(len(dat)) != expectedDatSize {
		datFile.Close()
		idx.Close()
		return nil, fmt.Errorf("dat v1 size mismatch: got %d, want %d (txCount=%d)",
			len(dat), expectedDatSize, txCount)
	}
	return &TxSegment{
		startBlock: startBlock,
		endBlock:   startBlock + SegmentSize,
		idx:        idx,
		reader:     recsplit.NewIndexReader(idx),
		datFile:    datFile,
		dat:        dat,
		txCount:    txCount,
	}, nil
}

func openSegmentV2(idx *recsplit.Index, datFile *os.File, dat []byte, startBlock uint64) (*TxSegment, error) {
	// V2 header: [4]magic + [4]blockCount + [8]txCount + [variable]EliasFano
	blockCount := uint64(binary.LittleEndian.Uint32(dat[4:8]))
	txCount := binary.LittleEndian.Uint64(dat[8:16])

	if txCount != idx.KeyCount() {
		datFile.Close()
		idx.Close()
		return nil, fmt.Errorf("dat v2 txCount mismatch: dat=%d idx=%d", txCount, idx.KeyCount())
	}

	var ef *eliasfano32.EliasFano
	if blockCount > 0 && txCount > 0 {
		// EF needs at least 16 bytes (count + u) plus data.
		if len(dat) < 32 {
			datFile.Close()
			idx.Close()
			return nil, fmt.Errorf("dat v2 truncated: %d bytes", len(dat))
		}
		ef, _ = eliasfano32.ReadEliasFano(dat[16:])
	}

	return &TxSegment{
		startBlock: startBlock,
		endBlock:   startBlock + blockCount,
		idx:        idx,
		reader:     recsplit.NewIndexReader(idx),
		datFile:    datFile,
		ef:         ef,
		blockCount: blockCount,
		txCount:    txCount,
	}, nil
}

// Lookup returns the block number for txHash, or nil if not found.
func (s *TxSegment) Lookup(txHash types.Hash) *uint64 {
	if s.idx.Empty() {
		return nil
	}
	enumOrdinal, found := s.reader.Lookup(txHash[:])
	if !found {
		return nil
	}
	if enumOrdinal >= s.txCount {
		return nil
	}

	if s.ef != nil {
		return s.lookupV2(enumOrdinal)
	}
	return s.lookupV1(enumOrdinal)
}

func (s *TxSegment) lookupV1(ordinal uint64) *uint64 {
	offset := ordinal * 4
	if offset+4 > uint64(len(s.dat)) {
		return nil
	}
	relBlock := binary.LittleEndian.Uint32(s.dat[offset : offset+4])
	blockNum := s.startBlock + uint64(relBlock)
	return &blockNum
}

// lookupV2 maps ordinal → block number via binary search on Elias-Fano
// block boundaries. The EF sequence stores cumulative tx counts:
//   ef[i] = total txs in blocks [startBlock, startBlock+i)
// So block for ordinal o = largest i where ef[i] <= o.
func (s *TxSegment) lookupV2(ordinal uint64) *uint64 {
	// Binary search: find first i where ef[i] > ordinal → block = i - 1.
	// ef has blockCount+1 entries: ef[0]=0, ef[blockCount]=txCount.
	n := int(s.blockCount + 1)
	blockIdx := sort.Search(n, func(i int) bool {
		return s.ef.Get(uint64(i)) > ordinal
	}) - 1

	if blockIdx < 0 || uint64(blockIdx) >= s.blockCount {
		return nil
	}
	blockNum := s.startBlock + uint64(blockIdx)
	return &blockNum
}

// TxCount returns the number of transactions indexed in this segment.
func (s *TxSegment) TxCount() uint64 { return s.txCount }

// StartBlock returns the first block in this segment.
func (s *TxSegment) StartBlock() uint64 { return s.startBlock }

// IsV2 returns true if this segment uses Elias-Fano compression.
func (s *TxSegment) IsV2() bool { return s.ef != nil }

// Close releases all resources.
func (s *TxSegment) Close() {
	if s.idx != nil {
		s.idx.Close()
	}
	if s.datFile != nil {
		s.datFile.Close()
	}
}
