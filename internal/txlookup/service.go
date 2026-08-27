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
	"sync"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/recsplit/eliasfano32"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

// TxVerifier reports whether block blockNum actually contains a tx with hash
// txHash (and if so its index). It is supplied by the wiring that has body
// access. Its purpose is to reject RecSplit phantom hits: an index built
// WITHOUT the LessFalsePositives fingerprint maps every out-of-set hash to some
// ordinal, so a newer segment will confidently return a wrong block for a tx
// that lives in an older one. With a verifier set, Lookup confirms each segment
// candidate and keeps probing older segments on a miss — making the cheaper
// no-LFP index (~4.4 bit/key) correct. nil verifier = legacy behaviour (return
// the first segment hit; correct only for LFP-on indexes).
type TxVerifier func(blockNum uint64, txHash types.Hash) (index uint64, present bool)

// Service provides tiered tx hash lookup (L0 MDBX + L1 RecSplit segments).
type Service struct {
	store    *cscompact.SegmentStoreReader
	segments []*txSegmentCached // sorted by startBlock descending
	verifier TxVerifier         // optional; rejects phantom hits (no-LFP indexes)
}

// SetVerifier installs the phantom-rejection verifier. Required for correctness
// when the segments were built with LessFalsePositives off.
func (s *Service) SetVerifier(v TxVerifier) { s.verifier = v }

type txSegmentCached struct {
	segNum     uint64
	startBlock uint64
	mu         sync.Mutex // protects the lazy load and its negative cache
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
	// Recorded ranges when the store has them: segments built from a live
	// source are sized by transaction count, so they are not all SegmentSize
	// wide and their start block cannot be derived from the segment number.
	// Their absence means the store predates variable sizing, where the
	// derivation is correct.
	ranges := readSegmentRanges(segmentDir)
	base := readSegmentBase(segmentDir)
	for i := uint64(0); i < store.SegmentCount(); i++ {
		start := base + i*SegmentSize
		if int(i) < len(ranges) {
			start = ranges[i].StartBlock
		}
		s.segments = append(s.segments, &txSegmentCached{
			segNum:     i,
			startBlock: start,
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
// tx may be nil to query only the L1 RecSplit segments (no hot MDBX tier).
func (s *Service) Lookup(tx kv.Tx, txHash types.Hash) (*uint64, error) {
	// L0: MDBX TxLookup table (exact key/value — no phantom, no verify needed).
	if tx != nil {
		v, err := tx.GetOne(modules.TxLookup, txHash[:])
		if err != nil {
			return nil, err
		}
		if len(v) > 0 {
			blockNum := decodeBlockNum(v)
			return &blockNum, nil
		}
	}

	// L1: RecSplit segments (newest first).
	for _, sc := range s.segments {
		sc.mu.Lock()
		if sc.seg == nil {
			if sc.loadFailed {
				sc.mu.Unlock()
				continue
			}
			seg, err := s.loadSegment(sc)
			if err != nil {
				log.Warn("Failed to load txlookup segment", "seg", sc.segNum, "err", err)
				sc.loadFailed = true
				sc.mu.Unlock()
				continue
			}
			sc.seg = seg
		}
		seg := sc.seg
		sc.mu.Unlock()
		if result := seg.Lookup(txHash); result != nil {
			if s.verifier == nil {
				return result, nil
			}
			// no-LFP index: confirm the candidate block really holds this hash;
			// a phantom (out-of-set hash mapped to a stranger's ordinal) fails
			// here and we keep probing older segments.
			if _, present := s.verifier(*result, txHash); present {
				return result, nil
			}
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
