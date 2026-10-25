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

// TxVerifierWithError preserves canonical-body read failures. A missing body
// for a segment candidate cannot be treated as a valid transaction miss.
type TxVerifierWithError func(blockNum uint64, txHash types.Hash) (index uint64, present bool, err error)

// TxPositionVerifier checks one transaction at a known ordinal within a
// canonical block. It must report body read failures as errors.
type TxPositionVerifier func(blockNum, txIndex uint64, txHash types.Hash) (present bool, err error)

// CanonicalAnchorReader returns the canonical block hash at a given height.
type CanonicalAnchorReader func(blockNum uint64) (types.Hash, error)

// Service provides tiered tx hash lookup (L0 MDBX + L1 RecSplit segments).
type Service struct {
	store             *cscompact.SegmentStoreReader
	segments          []*txSegmentCached // sorted by startBlock descending
	verifier          TxVerifier         // optional; rejects phantom hits (no-LFP indexes)
	verifierWithError TxVerifierWithError
	positionVerifier  TxPositionVerifier
	firstStart        uint64
	sealedEnd         uint64
}

// SetVerifier installs the phantom-rejection verifier. Required for correctness
// when the segments were built with LessFalsePositives off.
func (s *Service) SetVerifier(v TxVerifier) { s.verifier = v }

func (s *Service) SetVerifierWithError(v TxVerifierWithError) { s.verifierWithError = v }
func (s *Service) SetPositionVerifier(v TxPositionVerifier)   { s.positionVerifier = v }

type txSegmentCached struct {
	segNum     uint64
	startBlock uint64
	mu         sync.Mutex // protects the lazy load and its negative cache
	seg        *TxSegment // lazy loaded
	loadErr    error      // fail closed after a data/index read failure
}

// NewService opens all existing segments using SegmentStoreReader.
func NewService(segmentDir string) (*Service, error) {
	return openService(segmentDir, nil, false)
}

// NewLegacyService is the native indexer's admission path. A native owner
// cannot skip a canonical tail rebuild using EFD3 without checking anchors.
func NewLegacyService(segmentDir string) (*Service, error) {
	return openService(segmentDir, nil, true)
}

// NewAnchoredService is for an explicit owner. It refuses legacy segments and
// checks every committed anchor before Coverage can skip canonical rebuilding.
func NewAnchoredService(segmentDir string, identity types.Hash, canonical CanonicalAnchorReader, durableHead uint64) (*Service, error) {
	if identity == (types.Hash{}) || canonical == nil {
		return nil, fmt.Errorf("canonical anchor identity and reader required")
	}
	return openService(segmentDir, &ownerAnchor{identity: identity, canonical: canonical, durableHead: durableHead}, false)
}

type ownerAnchor struct {
	identity    types.Hash
	canonical   CanonicalAnchorReader
	durableHead uint64
}

func openService(segmentDir string, owner *ownerAnchor, legacyOnly bool) (*Service, error) {
	store, err := cscompact.OpenSegmentStore(segmentDir, "txindex")
	if err != nil {
		return nil, err
	}

	s := &Service{store: store}
	// The cidx is the publication point. For V2, each committed frame carries
	// its own block count and its embedded RecSplit index carries BaseDataID.
	// A sidecar append can be lost after cidx publication, so never infer a
	// variable-width segment's start from its ordinal or txindex.ranges.
	var previousEnd uint64
	for i := uint64(0); i < store.SegmentCount(); i++ {
		data, err := store.ReadSegmentData(i)
		if err != nil {
			store.Close()
			return nil, fmt.Errorf("read txindex segment %d: %w", i, err)
		}
		if err := rejectUnknownDatMagic(data); err != nil {
			store.Close()
			return nil, fmt.Errorf("txindex segment %d: %w", i, err)
		}
		var ri []byte
		if len(data) >= 4 && string(data[:4]) == string(datMagicV3[:]) {
			ri, err = store.ReadRecSplitBytes(i)
			if err != nil {
				store.Close()
				return nil, fmt.Errorf("read txindex index %d: %w", i, err)
			}
			if err := verifyV3Digest(data, ri); err != nil {
				store.Close()
				return nil, fmt.Errorf("txindex segment %d: %w", i, err)
			}
		}
		idx, err := store.GetRecSplitIndex(i)
		if err != nil {
			store.Close()
			return nil, fmt.Errorf("read txindex index %d: %w", i, err)
		}
		start := idx.BaseDataID()
		payload, err := decodeSegmentPayload(data, idx, ri)
		if err != nil {
			store.Close()
			return nil, fmt.Errorf("txindex segment %d: %w", i, err)
		}
		count := payload.blockCount
		if legacyOnly && payload.version == 3 {
			store.Close()
			return nil, fmt.Errorf("native txindex owner cannot admit anchored EFD3 segment %d", i)
		}
		if owner != nil {
			if payload.version != 3 {
				store.Close()
				return nil, fmt.Errorf("txindex segment %d is unanchored EFD%d", i, payload.version)
			}
			if payload.identity != owner.identity {
				store.Close()
				return nil, fmt.Errorf("txindex segment %d chain identity mismatch", i)
			}
			if start+count-1 > owner.durableHead {
				store.Close()
				return nil, fmt.Errorf("txindex segment %d exceeds durable canonical head", i)
			}
			actual, err := owner.canonical(start + count - 1)
			if err != nil {
				store.Close()
				return nil, fmt.Errorf("txindex segment %d canonical hash: %w", i, err)
			}
			if actual == (types.Hash{}) || actual != payload.endHash {
				store.Close()
				return nil, fmt.Errorf("txindex segment %d canonical hash mismatch", i)
			}
		}
		if count == 0 || start > ^uint64(0)-count || (i > 0 && start != previousEnd) {
			store.Close()
			return nil, fmt.Errorf("txindex segment %d: noncontiguous or invalid range start=%d count=%d previousEnd=%d", i, start, count, previousEnd)
		}
		previousEnd = start + count
		if i == 0 {
			s.firstStart = start
		}
		s.segments = append(s.segments, &txSegmentCached{
			segNum:     i,
			startBlock: start,
		})
	}
	s.sealedEnd = previousEnd

	// Sort newest first.
	sort.Slice(s.segments, func(i, j int) bool {
		return s.segments[i].startBlock > s.segments[j].startBlock
	})

	log.Info("TxLookup segments loaded", "count", len(s.segments))
	return s, nil
}

// Coverage is the physically validated contiguous segment range. Empty
// stores return (0, 0).
func (s *Service) Coverage() (uint64, uint64) { return s.firstStart, s.sealedEnd }

// SealedEndChecked derives the committed prefix from the same physical
// inventory NewService validates. A malformed or partial store must stop an
// owner startup rather than cause a skipped rebuild or a silent table miss.
func SealedEndChecked(dir string) (uint64, error) {
	s, err := NewService(dir)
	if err != nil {
		return 0, err
	}
	defer s.Close()
	return s.sealedEnd, nil
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
			if sc.loadErr != nil {
				err := sc.loadErr
				sc.mu.Unlock()
				return nil, err
			}
			seg, err := s.loadSegment(sc)
			if err != nil {
				sc.loadErr = fmt.Errorf("load txlookup segment %d: %w", sc.segNum, err)
				sc.mu.Unlock()
				return nil, sc.loadErr
			}
			sc.seg = seg
		}
		seg := sc.seg
		sc.mu.Unlock()
		if blockNum, txIndex, found := seg.LookupPosition(txHash); found {
			result := &blockNum
			if s.positionVerifier != nil && seg.version >= 2 {
				present, err := s.positionVerifier(blockNum, txIndex, txHash)
				if err != nil {
					return nil, err
				}
				if present {
					return result, nil
				}
				continue
			}
			if s.verifierWithError != nil {
				_, present, err := s.verifierWithError(*result, txHash)
				if err != nil {
					return nil, err
				}
				if present {
					return result, nil
				}
				continue
			}
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

// validateSegmentPayload runs before an owner may skip canonical tail
// reconstruction through this segment. A valid range header alone is not
// enough: a corrupt body/index would later turn into a silent lookup miss.
func validatedEliasFano(data []byte, blockCount, txCount uint64) (ef *eliasfano32.EliasFano, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			ef = nil
			err = fmt.Errorf("invalid Elias-Fano data: %v", recovered)
		}
	}()
	if binary.BigEndian.Uint64(data[:8]) != blockCount || binary.BigEndian.Uint64(data[8:16]) != txCount+1 {
		return nil, fmt.Errorf("Elias-Fano header differs from segment counts")
	}
	ef, used := eliasfano32.ReadEliasFano(data)
	if used != len(data) || ef.Count() != blockCount+1 || ef.Max() != txCount {
		return nil, fmt.Errorf("Elias-Fano shape differs from segment counts")
	}
	var previous uint64
	for n := uint64(0); n <= blockCount; n++ {
		v := ef.Get(n)
		if (n == 0 && v != 0) || v < previous || v > txCount {
			return nil, fmt.Errorf("Elias-Fano block boundary %d invalid", n)
		}
		previous = v
	}
	if previous != txCount {
		return nil, fmt.Errorf("Elias-Fano terminal offset differs from transaction count")
	}
	return ef, nil
}

func (s *Service) loadSegment(sc *txSegmentCached) (*TxSegment, error) {
	data, err := s.store.ReadSegmentData(sc.segNum)
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownDatMagic(data); err != nil {
		return nil, err
	}
	var ri []byte
	if len(data) >= 4 && string(data[:4]) == string(datMagicV3[:]) {
		ri, err = s.store.ReadRecSplitBytes(sc.segNum)
		if err != nil {
			return nil, err
		}
		if err := verifyV3Digest(data, ri); err != nil {
			return nil, err
		}
	}
	reader, err := s.store.GetRecSplitReader(sc.segNum)
	if err != nil {
		return nil, err
	}
	idx, _ := s.store.GetRecSplitIndex(sc.segNum)

	decoded, err := decodeSegmentPayload(data, idx, ri)
	if err != nil {
		return nil, err
	}
	seg := &TxSegment{
		startBlock: sc.startBlock,
		endBlock:   sc.startBlock + decoded.blockCount,
		idx:        idx,
		reader:     reader,
		blockCount: decoded.blockCount,
		txCount:    decoded.txCount,
		ef:         decoded.ef,
		dat:        decoded.raw,
		version:    decoded.version,
		identity:   decoded.identity,
		endHash:    decoded.endHash,
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
