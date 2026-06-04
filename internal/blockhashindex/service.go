// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Package blockhashindex is a cold-tier blockHash → blockNumber index built on
// the same cscompact RecSplit segment store as internal/txlookup, applying the
// hash self-verification pattern: the index stores NO 32-byte hash. A query
// carries the blockHash, so we only need MPHF(blockHash) → blockNumber, then
// confirm by recomputing the resolved block's header hash == the query.
//
// Layout per 1M-block segment: just a RecSplit MPHF over the block hashes with
// AddKey(blockHash, relBlock). RecSplit (Enums off) returns the stored offset
// directly, so Lookup(blockHash) → relBlock → blockNumber = startBlock+relBlock.
// There is NO dat array (unlike txindex, where many txs map to one block and an
// Elias-Fano ordinal→block map is needed); here it is 1:1.
//
// With LessFalsePositives off, every out-of-set hash phantom-hits some relBlock,
// so a verifier (recompute header hash) is REQUIRED for correctness: a phantom
// is rejected and the newest-first probe continues to older segments.
package blockhashindex

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/lib/recsplit"
	"github.com/n42blockchain/N42/log"
)

// SegmentSize is the number of blocks per segment (matches txlookup).
const SegmentSize = 1_000_000

// Verifier reports whether block blockNum's header hashes to blockHash. Supplied
// by the wiring that can read headers; it is what makes the no-fingerprint index
// correct (reject phantom hits) and self-verifying.
type Verifier func(blockNum uint64, blockHash types.Hash) bool

// Service resolves blockHash → blockNumber via mmap'd RecSplit segments.
type Service struct {
	store    *cscompact.SegmentStoreReader
	segments []*segCached // newest first
	verifier Verifier
}

type segCached struct {
	segNum     uint64
	startBlock uint64
	idx        *recsplit.Index
	reader     *recsplit.IndexReader
	keyCount   uint64
	loaded     bool
	loadFailed bool
}

// NewService opens all segments in dir (prefix "blockhash"). A windowed index
// records its first block in <dir>/blockhash.base (see txlookup.txindex.base).
func NewService(dir string) (*Service, error) {
	store, err := cscompact.OpenSegmentStore(dir, "blockhash")
	if err != nil {
		return nil, err
	}
	s := &Service{store: store}
	base := readBase(dir)
	for i := uint64(0); i < store.SegmentCount(); i++ {
		s.segments = append(s.segments, &segCached{
			segNum:     i,
			startBlock: base + i*SegmentSize,
		})
	}
	sort.Slice(s.segments, func(i, j int) bool {
		return s.segments[i].startBlock > s.segments[j].startBlock
	})
	log.Info("blockhashindex segments loaded", "count", len(s.segments), "base", base)
	return s, nil
}

// SetVerifier installs the header-hash verifier (required for correctness when
// the segments were built without LessFalsePositives).
func (s *Service) SetVerifier(v Verifier) { s.verifier = v }

// Lookup resolves blockHash → blockNumber, or nil if absent. Probes newest
// segment first; a verifier (if set) rejects phantom hits and the probe
// continues to older segments.
func (s *Service) Lookup(blockHash types.Hash) *uint64 {
	for _, sc := range s.segments {
		if !sc.loaded {
			if sc.loadFailed {
				continue
			}
			if err := s.load(sc); err != nil {
				log.Warn("blockhashindex: load segment failed", "seg", sc.segNum, "err", err)
				sc.loadFailed = true
				continue
			}
		}
		if sc.idx == nil || sc.idx.Empty() {
			continue
		}
		off, found := sc.reader.Lookup(blockHash[:])
		if !found || off >= sc.keyCount {
			continue
		}
		blockNum := sc.startBlock + off
		if s.verifier == nil || s.verifier(blockNum, blockHash) {
			return &blockNum
		}
	}
	return nil
}

func (s *Service) load(sc *segCached) error {
	idx, err := s.store.GetRecSplitIndex(sc.segNum)
	if err != nil {
		return err
	}
	sc.idx = idx
	sc.reader = recsplit.NewIndexReader(idx)
	sc.keyCount = idx.KeyCount()
	sc.loaded = true
	return nil
}

func (s *Service) SegmentCount() int { return len(s.segments) }

func (s *Service) Close() { s.store.Close() }

// readBase mirrors txlookup: <dir>/blockhash.base holds segment 0's first block
// for a windowed (EIP-4444) index; absent → 0 (archive).
func readBase(dir string) uint64 {
	b, err := os.ReadFile(filepath.Join(dir, "blockhash.base"))
	if err != nil {
		return 0
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0
	}
	return v
}
