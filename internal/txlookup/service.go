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
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

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
	loadFailed bool       // negative cache: don't retry after failure
}

// NewService opens all existing segments using SegmentStoreReader.
func NewService(segmentDir string) (*Service, error) {
	store, err := cscompact.OpenSegmentStore(segmentDir, "txindex")
	if err != nil {
		return nil, err
	}

	s := &Service{store: store}
	base := readSegmentBase(segmentDir)
	for i := uint64(0); i < store.SegmentCount(); i++ {
		s.segments = append(s.segments, &txSegmentCached{
			segNum:     i,
			startBlock: base + i*SegmentSize,
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
			if sc.loadFailed {
				continue
			}
			seg, err := s.loadSegment(sc)
			if err != nil {
				log.Warn("Failed to load txlookup segment", "seg", sc.segNum, "err", err)
				sc.loadFailed = true
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

// readSegmentBase returns the first block covered by segment 0. A full
// (archive) txindex starts at block 0; a windowed (EIP-4444) txindex covering
// only recent blocks records its base block in <dir>/txindex.base so the
// per-segment startBlock derivation (base + i*SegmentSize) stays correct.
// Absent / unparesable file → 0 (legacy archive behaviour).
func readSegmentBase(dir string) uint64 {
	b, err := os.ReadFile(filepath.Join(dir, "txindex.base"))
	if err != nil {
		return 0
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func decodeBlockNum(v []byte) uint64 {
	n := uint64(0)
	for _, b := range v {
		n = n<<8 | uint64(b)
	}
	return n
}
