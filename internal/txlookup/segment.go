// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// segment.go provides read-only access to a RecSplit-indexed tx hash segment.
// Each segment covers a range of blocks and maps txHash → blockNumber in O(1).

package txlookup

import (
	"encoding/binary"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/recsplit"
)

// SegmentSize is the number of blocks per L1 segment.
// 1M blocks balances file count (20 files for full chain) vs max segment
// size (~2 GB for tx-dense ranges). Larger segments reduce miss-scan cost.
const SegmentSize = 1_000_000

// TxSegment is a read-only RecSplit-indexed tx hash lookup segment.
// It maps txHash → blockNumber for a contiguous range of blocks.
type TxSegment struct {
	startBlock uint64
	endBlock   uint64
	idx        *recsplit.Index
	reader     *recsplit.IndexReader
	dat        []byte   // mmap'd blockNumber array (4 bytes per tx, relative to startBlock)
	datFile    *os.File // keep open for mmap lifetime
}

// OpenSegment opens a segment's .idx and .dat files.
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
	if _, err := datFile.ReadAt(dat, 0); err != nil {
		datFile.Close()
		idx.Close()
		return nil, fmt.Errorf("read dat: %w", err)
	}

	txCount := idx.KeyCount()
	expectedDatSize := int64(txCount) * 4
	if fi.Size() != expectedDatSize {
		datFile.Close()
		idx.Close()
		return nil, fmt.Errorf("dat size mismatch: got %d, want %d (txCount=%d)",
			fi.Size(), expectedDatSize, txCount)
	}

	// Extract startBlock from idx BaseDataID.
	startBlock := idx.BaseDataID()

	return &TxSegment{
		startBlock: startBlock,
		endBlock:   startBlock + SegmentSize,
		idx:        idx,
		reader:     recsplit.NewIndexReader(idx),
		dat:        dat,
		datFile:    datFile,
	}, nil
}

// Lookup returns the block number for txHash, or nil if not found.
// Uses RecSplit existence filter (0.4% FPR) to avoid unnecessary dat reads.
func (s *TxSegment) Lookup(txHash types.Hash) *uint64 {
	if s.idx.Empty() {
		return nil
	}
	enumOrdinal, found := s.reader.Lookup(txHash[:])
	if !found {
		return nil // existence filter rejected
	}
	if enumOrdinal >= s.idx.KeyCount() {
		return nil // out of range (shouldn't happen)
	}

	// Read relative blockNum from dat array.
	offset := enumOrdinal * 4
	if offset+4 > uint64(len(s.dat)) {
		return nil
	}
	relBlock := binary.LittleEndian.Uint32(s.dat[offset : offset+4])
	blockNum := s.startBlock + uint64(relBlock)
	return &blockNum
}

// TxCount returns the number of transactions indexed in this segment.
func (s *TxSegment) TxCount() uint64 { return s.idx.KeyCount() }

// StartBlock returns the first block in this segment.
func (s *TxSegment) StartBlock() uint64 { return s.startBlock }

// Close releases all resources.
func (s *TxSegment) Close() {
	if s.idx != nil {
		s.idx.Close()
	}
	if s.datFile != nil {
		s.datFile.Close()
	}
}
