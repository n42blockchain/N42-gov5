// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// service.go provides a tiered tx hash → block number lookup service.
//
// L0 (hot): MDBX TxLookup table for recent blocks (real-time writes).
// L1 (cold): RecSplit segments for historical blocks (read-only, O(1) lookup).
//
// Query flow: L0 → L1 (newest segment first) → not found.

package txlookup

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

// Service provides tiered tx hash lookup (L0 MDBX + L1 RecSplit segments).
type Service struct {
	segmentDir string
	segments   []*TxSegment // sorted by startBlock descending (newest first)
}

// NewService opens all existing segments in the given directory.
func NewService(segmentDir string) (*Service, error) {
	s := &Service{segmentDir: segmentDir}
	if err := s.loadSegments(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) loadSegments() error {
	entries, err := os.ReadDir(s.segmentDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no segments yet
		}
		return err
	}

	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".idx") {
			continue
		}
		if !strings.HasPrefix(e.Name(), "txlookup-") {
			continue
		}
		baseName := strings.TrimSuffix(e.Name(), ".idx")
		idxPath := filepath.Join(s.segmentDir, baseName+".idx")
		datPath := filepath.Join(s.segmentDir, baseName+".dat")

		if _, err := os.Stat(datPath); err != nil {
			continue // incomplete segment
		}

		seg, err := OpenSegment(idxPath, datPath)
		if err != nil {
			log.Warn("Failed to open tx segment", "name", baseName, "err", err)
			continue
		}
		s.segments = append(s.segments, seg)
	}

	// Sort by startBlock descending — query newest segments first.
	sort.Slice(s.segments, func(i, j int) bool {
		return s.segments[i].startBlock > s.segments[j].startBlock
	})

	log.Info("TxLookup segments loaded", "count", len(s.segments))
	return nil
}

// Lookup queries L0 (MDBX) then L1 (segments) for txHash → blockNumber.
func (s *Service) Lookup(tx kv.Tx, txHash types.Hash) (*uint64, error) {
	// L0: MDBX TxLookup table.
	v, err := tx.GetOne(modules.TxLookup, txHash[:])
	if err != nil {
		return nil, err
	}
	if len(v) > 0 {
		blockNum := decodeBlockNum(v)
		return &blockNum, nil
	}

	// L1: RecSplit segments (newest first).
	for _, seg := range s.segments {
		if result := seg.Lookup(txHash); result != nil {
			return result, nil
		}
	}

	return nil, nil // not found
}

// SegmentCount returns the number of loaded L1 segments.
func (s *Service) SegmentCount() int { return len(s.segments) }

// Stats returns summary statistics.
func (s *Service) Stats() string {
	totalTx := uint64(0)
	for _, seg := range s.segments {
		totalTx += seg.TxCount()
	}
	return fmt.Sprintf("segments=%d totalTx=%d", len(s.segments), totalTx)
}

// Close releases all segment resources.
func (s *Service) Close() {
	for _, seg := range s.segments {
		seg.Close()
	}
	s.segments = nil
}

// decodeBlockNum decodes a big-endian block number from TxLookup value.
func decodeBlockNum(v []byte) uint64 {
	// TxLookup values are big-endian encoded uint256 (variable length).
	n := uint64(0)
	for _, b := range v {
		n = n<<8 | uint64(b)
	}
	return n
}
