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
var datMagicV3 = [4]byte{'E', 'F', 'D', '3'}

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
	version    uint8
	identity   types.Hash
	endHash    types.Hash

	// V1 fallback: raw uint32 array.
	dat []byte // nil when using V2
}

// OpenSegment opens a segment's .idx and .dat files.
// Auto-detects V1 (raw) vs V2 (Elias-Fano) format.
func OpenSegment(idxPath, datPath string) (*TxSegment, error) {
	ri, err := os.ReadFile(idxPath)
	if err != nil {
		return nil, fmt.Errorf("read idx: %w", err)
	}
	digestDat, err := os.ReadFile(datPath)
	if err != nil {
		return nil, fmt.Errorf("read dat: %w", err)
	}
	if err := rejectUnknownDatMagic(digestDat); err != nil {
		return nil, err
	}
	if len(digestDat) >= 4 && string(digestDat[:4]) == string(datMagicV3[:]) {
		if err := verifyV3Digest(digestDat, ri); err != nil {
			return nil, err
		}
	}
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

	decoded, err := decodeSegmentPayload(dat, idx, ri)
	if err != nil {
		datFile.Close()
		idx.Close()
		return nil, err
	}
	return &TxSegment{startBlock: startBlock, endBlock: startBlock + decoded.blockCount,
		idx: idx, reader: recsplit.NewIndexReader(idx), datFile: datFile,
		ef: decoded.ef, dat: decoded.raw, blockCount: decoded.blockCount,
		txCount: decoded.txCount, version: decoded.version,
		identity: decoded.identity, endHash: decoded.endHash}, nil
}

// Lookup returns the block number for txHash, or nil if not found.
func (s *TxSegment) Lookup(txHash types.Hash) *uint64 {
	block, _, found := s.LookupPosition(txHash)
	if !found {
		return nil
	}
	return &block
}

// LookupPosition returns a candidate block and transaction offset. The offset
// is exact for Elias-Fano segments, whose ordinals follow block transaction
// order. V1 has no block boundary table and returns an unknown offset.
func (s *TxSegment) LookupPosition(txHash types.Hash) (uint64, uint64, bool) {
	if s.idx.Empty() {
		return 0, 0, false
	}
	enumOrdinal, found := s.reader.Lookup(txHash[:])
	if !found {
		return 0, 0, false
	}
	if enumOrdinal >= s.txCount {
		return 0, 0, false
	}

	if s.ef != nil {
		block, offset, ok := s.lookupV2Position(enumOrdinal)
		return block, offset, ok
	}
	block := s.lookupV1(enumOrdinal)
	if block == nil {
		return 0, 0, false
	}
	return *block, 0, true
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
//
//	ef[i] = total txs in blocks [startBlock, startBlock+i)
//
// So block for ordinal o = largest i where ef[i] <= o.
func (s *TxSegment) lookupV2(ordinal uint64) *uint64 {
	block, _, ok := s.lookupV2Position(ordinal)
	if !ok {
		return nil
	}
	return &block
}

func (s *TxSegment) lookupV2Position(ordinal uint64) (uint64, uint64, bool) {
	// Binary search: find first i where ef[i] > ordinal → block = i - 1.
	// ef has blockCount+1 entries: ef[0]=0, ef[blockCount]=txCount.
	n := int(s.blockCount + 1)
	blockIdx := sort.Search(n, func(i int) bool {
		return s.ef.Get(uint64(i)) > ordinal
	}) - 1

	if blockIdx < 0 || uint64(blockIdx) >= s.blockCount {
		return 0, 0, false
	}
	blockNum := s.startBlock + uint64(blockIdx)
	return blockNum, ordinal - s.ef.Get(uint64(blockIdx)), true
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
