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

// HeadersBodiesSource is the exported handle for cross-package callers
// (cmd/ethexec sender-recovery, future tools). Same interface as the
// internal one but exported method names. Constructed via
// OpenHeadersBodiesSource.
type HeadersBodiesSource interface {
	Header(blockNum uint64) (*block.Header, error)
	Body(blockNum uint64) (*GethBodyResult, error)
	MaxBlock() uint64
	Close()
}

// exportedSource adapts the unexported headersBodiesSource to the
// exported interface. Keeps internal call sites untouched while giving
// external callers a stable API.
type exportedSource struct{ s headersBodiesSource }

func (e *exportedSource) Header(n uint64) (*block.Header, error) { return e.s.header(n) }
func (e *exportedSource) Body(n uint64) (*GethBodyResult, error) { return e.s.body(n) }
func (e *exportedSource) MaxBlock() uint64                       { return e.s.maxBlock() }
func (e *exportedSource) Close()                                 { e.s.close() }

// internalSource adapts the exported HeadersBodiesSource back to the
// unexported interface so SenderStage (which uses the unexported one
// internally for shared close lifecycle) can accept either.
type internalSource struct{ s HeadersBodiesSource }

func (i *internalSource) header(n uint64) (*block.Header, error) { return i.s.Header(n) }
func (i *internalSource) body(n uint64) (*GethBodyResult, error) { return i.s.Body(n) }
func (i *internalSource) maxBlock() uint64                       { return i.s.MaxBlock() }
func (i *internalSource) close()                                 { i.s.Close() }

// OpenHeadersBodiesSource picks the right backend by probing dir:
// N42 columnar (headerc.cidx present) or geth ancient (default).
// The returned source's Close() releases all underlying handles.
func OpenHeadersBodiesSource(dir string) (HeadersBodiesSource, error) {
	src, err := openHeadersBodiesSource(dir)
	if err != nil {
		return nil, err
	}
	return &exportedSource{s: src}, nil
}

// openHeadersBodiesSource picks the reader implementation by probing
// the input directory.
//
//   - N42 columnar (n42CompactSource, header_compact.go +
//     body_compact.go): stored as headerc.cidx + headerc.NNNN.cdat /
//     bodyc.cidx + bodyc.NNNN.cdat. Each block-field is its own column,
//     8192-block zstd segments. The trailer of each segment carries
//     the canonical Hash() per block so readers don't reconstruct
//     ParentHash + Bloom from receipts.
//
//   - geth ancient / standard freezer (gethFreezerSource): full RLP
//     per block, 64-block zstd batches at headers.NNNN.cdat /
//     bodies.NNNN.cdat. DecodeGethHeader returns canonical Header
//     directly.
//
// The earlier shared filename layout (both formats using
// headers.NNNN.cdat) was bug-prone — renaming the columnar archive
// to headerc/bodyc makes the two formats unambiguous.
func openHeadersBodiesSource(dir string) (headersBodiesSource, error) {
	if _, err := os.Stat(filepath.Join(dir, "headerc.cidx")); err == nil {
		return openN42CompactSource(dir)
	}
	return openGethFreezerSource(dir)
}

// gethFreezerSource adapts freezer.Freezer to headersBodiesSource.
type gethFreezerSource struct {
	f *freezer.Freezer
}

func openGethFreezerSource(dir string) (*gethFreezerSource, error) {
	f, err := freezer.NewReadOnly(dir)
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
// The segment trailer carries the canonical Hash() per block, so
// reading is O(1) — no parent-chain walk, no bloom recompute, no
// external receipts dependency. ParentHash and Bloom on the returned
// Header remain zero (the columnar format drops them); callers that
// only need Hash() get the right value via the cached atomic.Value.
type n42CompactSource struct {
	hr *HeaderCompactReader
	br *BodyCompactReader
}

func openN42CompactSource(dir string) (*n42CompactSource, error) {
	hr, err := OpenHeaderCompact(dir)
	if err != nil {
		return nil, fmt.Errorf("open header compact %s: %w", dir, err)
	}
	br, err := OpenBodyCompact(dir)
	if err != nil {
		hr.Close()
		return nil, fmt.Errorf("open body compact %s: %w", dir, err)
	}
	// EIP-4444: a Full node keeps only a recent window of bodies hot; if a
	// cold-read resolver is installed, trimmed (cold) segments are fetched on
	// demand instead of failing with ErrBodyTrimmed.
	if defaultColdResolver != nil {
		br.SetColdResolver(defaultColdResolver)
	}
	return &n42CompactSource{hr: hr, br: br}, nil
}

func (s *n42CompactSource) header(n uint64) (*block.Header, error) {
	// Reader populates Header.hash atomic.Value from the segment
	// trailer (hfStoredHash flag), so hdr.Hash() returns canonical
	// directly. ParentHash and Bloom remain zero on the struct —
	// callers that need them must reconstruct externally; for
	// witness-replay only Hash() matters (BLOCKHASH).
	return s.hr.ReadHeader(n)
}

func (s *n42CompactSource) body(n uint64) (*GethBodyResult, error) {
	db, err := s.br.ReadBody(n)
	if err != nil {
		return nil, err
	}
	var uncles []*block.Header
	if len(db.UncleRLP) > 0 {
		uncles = make([]*block.Header, len(db.UncleRLP))
		for i, raw := range db.UncleRLP {
			h, err := DecodeUncleHeader(raw)
			if err != nil {
				return nil, fmt.Errorf("uncle %d of block %d: %w", i, n, err)
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
}

// BlockHashWindowSize is the EVM BLOCKHASH look-back window per
// yellow paper H.2 (1..256 ancestors only).
const BlockHashWindowSize = 256
const blockHashWindowSize = BlockHashWindowSize // internal alias retained for existing callers

// MakeBlockHashFn builds a BLOCKHASH resolver from a snapshot of the
// most recent canonical hashes. recent[i] = hash of block (currentBlock
// - len(recent) + i). The closure does pure index lookup — safe to
// call from any goroutine, no source access. Exported so single-block
// tools (e.g. witness-block-trace) can build the same resolver shape
// the parallel pipeline uses.
func MakeBlockHashFn(currentBlock uint64, recent []types.Hash) func(uint64) types.Hash {
	return makeBlockHashFn(currentBlock, recent)
}

// makeBlockHashFn — see MakeBlockHashFn doc.
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
