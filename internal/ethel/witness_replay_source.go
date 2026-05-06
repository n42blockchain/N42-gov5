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

// openHeadersBodiesSource picks the reader implementation by probing
// the input directory.
//
//   - N42 columnar (n42CompactSource, header_compact.go +
//     body_compact.go): stored as hcol.cidx + hcol.NNNN.cdat /
//     bcol.cidx + bcol.NNNN.cdat. Each block-field is its own column,
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
// to hcol/bcol makes the two formats unambiguous.
func openHeadersBodiesSource(dir string) (headersBodiesSource, error) {
	if _, err := os.Stat(filepath.Join(dir, "hcol.cidx")); err == nil {
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
			h, err := decodeUncleHeader(raw)
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
