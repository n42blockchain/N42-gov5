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
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/recsplit/eliasfano32"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

// Service provides tiered tx hash lookup (L0 MDBX + L1 RecSplit segments).
type Service struct {
	store    *cscompact.SegmentStoreReader
	segments []*txSegmentCached // sorted by startBlock descending
}

type txSegmentCached struct {
	segNum     uint64
	startBlock uint64
	seg        *TxSegment // lazy loaded
}

// NewService opens all existing segments using SegmentStoreReader.
func NewService(segmentDir string) (*Service, error) {
	store, err := cscompact.OpenSegmentStore(segmentDir)
	if err != nil {
		return nil, err
	}

	s := &Service{store: store}
	for i := uint64(0); i < store.SegmentCount(); i++ {
		s.segments = append(s.segments, &txSegmentCached{
			segNum:     i,
			startBlock: i * SegmentSize,
		})
	}

	// Sort newest first.
	sort.Slice(s.segments, func(i, j int) bool {
		return s.segments[i].startBlock > s.segments[j].startBlock
	})

	log.Info("TxLookup segments loaded", "count", len(s.segments))
	return s, nil
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
	for _, sc := range s.segments {
		if sc.seg == nil {
			seg, err := s.loadSegment(sc)
			if err != nil {
				continue
			}
			sc.seg = seg
		}
		if result := sc.seg.Lookup(txHash); result != nil {
			return result, nil
		}
	}

	return nil, nil
}

func (s *Service) loadSegment(sc *txSegmentCached) (*TxSegment, error) {
	data, err := s.store.ReadSegmentData(sc.segNum)
	if err != nil {
		return nil, err
	}
	reader, err := s.store.GetRecSplitReader(sc.segNum)
	if err != nil {
		return nil, err
	}
	idx, _ := s.store.GetRecSplitIndex(sc.segNum)

	// Parse dat format (V1 or V2).
	seg := &TxSegment{
		startBlock: sc.startBlock,
		endBlock:   sc.startBlock + SegmentSize,
		idx:        idx,
		reader:     reader,
	}

	if len(data) >= 16 && string(data[:4]) == string(datMagicV2[:]) {
		// V2: Elias-Fano.
		seg.blockCount = uint64(binary.LittleEndian.Uint32(data[4:8]))
		seg.txCount = binary.LittleEndian.Uint64(data[8:16])
		seg.endBlock = seg.startBlock + seg.blockCount
		if seg.blockCount > 0 && seg.txCount > 0 && len(data) >= 32 {
			ef, _ := eliasfano32.ReadEliasFano(data[16:])
			seg.ef = ef
		}
	} else {
		// V1: raw uint32 array.
		seg.txCount = idx.KeyCount()
		seg.dat = data
	}
	return seg, nil
}

func (s *Service) SegmentCount() int { return len(s.segments) }

func (s *Service) Stats() string {
	return fmt.Sprintf("segments=%d", len(s.segments))
}

func (s *Service) Close() {
	s.store.Close()
}

func decodeBlockNum(v []byte) uint64 {
	n := uint64(0)
	for _, b := range v {
		n = n<<8 | uint64(b)
	}
	return n
}
