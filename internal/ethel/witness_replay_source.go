// Copyright 2022-2026 The N42 Authors
// witness_replay_source.go — abstracts how the witness-replay reader
// fetches headers + bodies. Two backends:
//
//   - gethFreezerSource wraps freezer.Freezer reading raw RLP from
//     headers.NNNN.dat / bodies.NNNN.dat (geth ancient store).
//   - n42CompactSource wraps HeaderCompactReader + BodyCompactReader
//     reading N42's columnar 8192-block-segment .cdat files.
//
// Auto-detected at pipeline open by probing for headers.cidx — N42's
// columnar header index. Fallback to geth Freezer if absent.

package ethel

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// headersBodiesSource is the read-side abstraction used by feedBlocks
// and the BLOCKHASH resolver.
type headersBodiesSource interface {
	header(blockNum uint64) (*block.Header, error)
	body(blockNum uint64) (*GethBodyResult, error)
	maxBlock() uint64
	close()
}

// openHeadersBodiesSource picks the reader implementation by probing
// the input directory. Both formats use headers.cidx as the index
// filename, but their layouts differ in entry size:
//
//   - N42 columnar (HeaderCompactStage): 8 bytes per 8192-block
//     segment. cidx for ~25M blocks is ~24 KB.
//   - geth ancient / freezer: 12 bytes per 64-block batch. cidx for
//     ~25M blocks is ~150 MB.
//
// We detect by file size — anything under 8 MB is treated as
// columnar (a wildly conservative cap; even a 5B-block columnar
// chain at 8 bytes/segment would be ~5 MB).
//
// The N42 columnar format strips Header.Bloom (it's recomputable
// from receipts), so the columnar reader needs a separate receipts
// source for bloom recovery. receiptsFromDir defaults to the
// headers/bodies dir but can be overridden when those receipts are
// incomplete (e.g., user falls back to geth ancient).
func openHeadersBodiesSource(dir, receiptsFromDir string) (headersBodiesSource, error) {
	if isN42ColumnarHeaders(dir) {
		if receiptsFromDir == "" {
			receiptsFromDir = dir
		}
		return openN42CompactSource(dir, receiptsFromDir)
	}
	return openGethFreezerSource(dir)
}

const columnarHeaderIdxMaxBytes = 8 * 1024 * 1024 // 8 MB — see openHeadersBodiesSource

func isN42ColumnarHeaders(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "headers.cidx"))
	if err != nil {
		return false
	}
	return st.Size() <= columnarHeaderIdxMaxBytes
}

// gethFreezerSource adapts freezer.Freezer to headersBodiesSource.
type gethFreezerSource struct {
	f *freezer.Freezer
}

func openGethFreezerSource(dir string) (*gethFreezerSource, error) {
	f, err := freezer.New(dir, 0)
	if err != nil {
		return nil, fmt.Errorf("open geth freezer %s: %w", dir, err)
	}
	return &gethFreezerSource{f: f}, nil
}

func (s *gethFreezerSource) header(n uint64) (*block.Header, error) {
	data, err := s.f.Ancient(freezer.TableHeaders, n)
	if err != nil {
		return nil, err
	}
	return DecodeGethHeader(data)
}

func (s *gethFreezerSource) body(n uint64) (*GethBodyResult, error) {
	data, err := s.f.Ancient(freezer.TableBodies, n)
	if err != nil {
		return nil, err
	}
	return DecodeGethBody(data)
}

func (s *gethFreezerSource) maxBlock() uint64 { return s.f.Frozen() }

func (s *gethFreezerSource) close() { s.f.Close() }

// freezer returns the underlying freezer for callers that still need
// direct Ancient access (e.g. witness/senders tables that share the dir).
func (s *gethFreezerSource) freezer() *freezer.Freezer { return s.f }

// n42CompactSource adapts the N42 columnar readers. Each per-block
// access decodes a segment if not cached; sequential reads stay hot.
//
// The columnar header format DROPS two fields that are needed to
// recompute the canonical Header.Hash() (and thus BLOCKHASH):
//
//   - ParentHash: must be set to the prior block's hash
//   - Bloom: must be recomputed from the block's receipts
//
// header(n) restores both: parentHash via hashCache[n-1] and bloom
// via the receipts.cdat at the same dir. Hash() then matches
// canonical Ethereum mainnet hashes byte-for-byte. Sequential
// feedBlocks access builds the cache naturally.
type n42CompactSource struct {
	hr          *HeaderCompactReader
	br          *BodyCompactReader
	receiptsF   *freezer.Freezer // for bloom recompute (geth-format receipts)
	receiptsTbl *freezer.FreezerTable
	receiptsFmt receiptsFormat

	mu        sync.RWMutex
	hashCache map[uint64]types.Hash
}

type receiptsFormat int

const (
	receiptsFormatNone receiptsFormat = iota
	receiptsFormatN42Compact
	receiptsFormatGethRLP
)

func openN42CompactSource(dir, receiptsDir string) (*n42CompactSource, error) {
	hr, err := OpenHeaderCompact(dir)
	if err != nil {
		return nil, fmt.Errorf("open header compact %s: %w", dir, err)
	}
	br, err := OpenBodyCompact(dir)
	if err != nil {
		hr.Close()
		return nil, fmt.Errorf("open body compact %s: %w", dir, err)
	}
	src := &n42CompactSource{
		hr:        hr,
		br:        br,
		hashCache: make(map[uint64]types.Hash, 4096),
	}
	// Both N42-compact and geth-ancient receipts dirs ship a
	// receipts.cidx, so we can't tell them apart by filename alone.
	// Probe a known-non-empty block (Frontier era: 46147 is the first
	// txed block) with each codec; whichever decodes cleanly wins.
	gf, err := freezer.New(receiptsDir, 0)
	if err != nil {
		hr.Close()
		br.Close()
		return nil, fmt.Errorf("open receipts dir %s: %w", receiptsDir, err)
	}
	const probeBlock = uint64(46147)
	probe, perr := gf.Ancient(freezer.TableReceipts, probeBlock)
	if perr == nil && len(probe) > 0 {
		// Geth RLP gets first try because geth ancient is the more
		// common fallback and DecodeReceiptsCompact happens to accept
		// short geth blobs without erroring.
		if _, derr := DecodeGethReceipts(probe); derr == nil {
			src.receiptsF = gf
			src.receiptsFmt = receiptsFormatGethRLP
			return src, nil
		}
		if _, derr := DecodeReceiptsCompact(probe); derr == nil {
			gf.Close()
			t, terr := freezer.NewFreezerTableCompressedReadOnly(receiptsDir, freezer.TableReceipts, "c")
			if terr == nil {
				t.ForceBatchSize(freezer.BatchSize)
				src.receiptsTbl = t
				src.receiptsFmt = receiptsFormatN42Compact
				return src, nil
			}
		}
	}
	// No probe block available or both decoders failed — keep generic
	// freezer open and assume geth RLP. Bloom recompute will fail
	// gracefully per-block if data is incompatible.
	src.receiptsF = gf
	src.receiptsFmt = receiptsFormatGethRLP
	return src, nil
}

func (s *n42CompactSource) header(n uint64) (*block.Header, error) {
	hdr, err := s.hr.ReadHeader(n)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		parent, err := s.hashOf(n - 1)
		if err != nil {
			return nil, fmt.Errorf("block %d: parent hash: %w", n, err)
		}
		hdr.ParentHash = parent
	}
	// Recompute bloom from receipts. The columnar format drops bloom
	// because it's derivable from logs; we need the canonical value
	// here so Hash() matches mainnet.
	if err := s.fillBloom(n, hdr); err != nil {
		return nil, fmt.Errorf("block %d: bloom: %w", n, err)
	}
	// The HeaderCompactReader returns a SHARED pointer into its
	// segment cache. We just mutated parentHash + bloom on it, so the
	// previously-cached hash atomic.Value is stale; clear it before
	// computing the canonical Hash().
	hdr.ResetHashCache()
	h := hdr.Hash()
	s.mu.Lock()
	s.hashCache[n] = h
	s.mu.Unlock()
	return hdr, nil
}

// fillBloom looks up the block's receipts and rebuilds Header.Bloom.
// The N42 columnar header reader returns zeros for Bloom; canonical
// hash needs the real value so BLOCKHASH from contracts agrees with
// what was recorded by ethexec.
func (s *n42CompactSource) fillBloom(n uint64, hdr *block.Header) error {
	var data []byte
	var err error
	switch s.receiptsFmt {
	case receiptsFormatN42Compact:
		if n >= s.receiptsTbl.Items() {
			return fmt.Errorf("block %d beyond receipts table items %d (incomplete archive)",
				n, s.receiptsTbl.Items())
		}
		data, err = s.receiptsTbl.Retrieve(n)
	case receiptsFormatGethRLP:
		data, err = s.receiptsF.Ancient(freezer.TableReceipts, n)
	default:
		return nil // no receipts source
	}
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil // empty block: zero bloom is correct
	}
	var receipts block.Receipts
	switch s.receiptsFmt {
	case receiptsFormatN42Compact:
		receipts, err = DecodeReceiptsCompact(data)
	case receiptsFormatGethRLP:
		receipts, err = DecodeGethReceipts(data)
	}
	if err != nil {
		return fmt.Errorf("decode receipts: %w", err)
	}
	hdr.Bloom = block.CreateBloom(receipts)
	return nil
}

// hashOf returns the canonical hash of block n, reading the chain
// down from n if cache misses. Sequential feedBlocks access keeps
// the cache populated forward, so this only recurses for the very
// first block of a fresh source.
func (s *n42CompactSource) hashOf(n uint64) (types.Hash, error) {
	s.mu.RLock()
	h, ok := s.hashCache[n]
	s.mu.RUnlock()
	if ok {
		return h, nil
	}
	hdr, err := s.header(n) // recursive; populates cache on the way down
	if err != nil {
		return types.Hash{}, err
	}
	return hdr.Hash(), nil
}

func (s *n42CompactSource) body(n uint64) (*GethBodyResult, error) {
	db, err := s.br.ReadBody(n)
	if err != nil {
		return nil, err
	}
	// DecodedBlock holds uncle RLP; witness-replay's GethBodyResult
	// expects decoded headers. Decode now (uncles are pre-merge only,
	// so this allocs only for the early chain).
	var uncles []*block.Header
	if len(db.UncleRLP) > 0 {
		uncles = make([]*block.Header, len(db.UncleRLP))
		for i, raw := range db.UncleRLP {
			h, err := DecodeGethHeader(raw)
			if err != nil {
				return nil, fmt.Errorf("decode uncle %d of block %d: %w", i, n, err)
			}
			uncles[i] = h
		}
	}
	return &GethBodyResult{
		Transactions: db.Txs,
		Uncles:       uncles,
		Withdrawals:  db.Withdrawals,
	}, nil
}

func (s *n42CompactSource) maxBlock() uint64 {
	m := s.hr.MaxBlock()
	if b := s.br.MaxBlock(); b < m {
		m = b
	}
	return m
}

func (s *n42CompactSource) close() {
	s.hr.Close()
	s.br.Close()
	if s.receiptsTbl != nil {
		s.receiptsTbl.Close()
	}
	if s.receiptsF != nil {
		s.receiptsF.Close()
	}
}

// blockHashWindowSize is the EVM BLOCKHASH look-back window per
// yellow paper H.2 (1..256 ancestors only).
const blockHashWindowSize = 256

// makeBlockHashFn builds a BLOCKHASH resolver from a snapshot of the
// most recent canonical hashes. recent[i] = hash of block (currentBlock
// - len(recent) + i). The closure does pure index lookup — safe to
// call from any goroutine, no source access. Caller (feedBlocks) is
// expected to maintain the sliding window single-threaded.
func makeBlockHashFn(currentBlock uint64, recent []types.Hash) func(uint64) types.Hash {
	snap := make([]types.Hash, len(recent))
	copy(snap, recent)
	base := currentBlock - uint64(len(snap))
	return func(n uint64) types.Hash {
		if n < base || n >= currentBlock || currentBlock-n > blockHashWindowSize {
			return types.Hash{}
		}
		return snap[n-base]
	}
}
