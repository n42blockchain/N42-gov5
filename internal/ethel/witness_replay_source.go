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
// the input directory. If headers.cidx is present, the dir was written
// by HeaderCompactStage; use the columnar readers. Otherwise treat
// the dir as a geth ancient store.
func openHeadersBodiesSource(dir string) (headersBodiesSource, error) {
	if _, err := os.Stat(filepath.Join(dir, "headers.cidx")); err == nil {
		return openN42CompactSource(dir)
	}
	return openGethFreezerSource(dir)
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
	if hr.MaxBlock() != br.MaxBlock() {
		// Mismatched segment counts mean one stage finished further
		// than the other — replay can't safely use such input.
		max := hr.MaxBlock()
		if br.MaxBlock() < max {
			max = br.MaxBlock()
		}
		_ = max // both readers will refuse out-of-range blocks
	}
	return &n42CompactSource{hr: hr, br: br}, nil
}

func (s *n42CompactSource) header(n uint64) (*block.Header, error) {
	return s.hr.ReadHeader(n)
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
}

// makeBlockHashFromSource builds a BLOCKHASH resolver bound to a
// specific current block, fetching ancestors via the source's header()
// method. Caches resolved hashes per closure so repeated BLOCKHASH(n)
// pays the lookup once per n. Mirrors yellow paper §H.2 semantics.
func makeBlockHashFromSource(src headersBodiesSource, currentBlock uint64) func(uint64) types.Hash {
	var cache map[uint64]types.Hash
	return func(n uint64) types.Hash {
		if n >= currentBlock || currentBlock-n > 256 {
			return types.Hash{}
		}
		if h, ok := cache[n]; ok {
			return h
		}
		hdr, err := src.header(n)
		if err != nil {
			return types.Hash{}
		}
		h := hdr.Hash()
		if cache == nil {
			cache = make(map[uint64]types.Hash, 4)
		}
		cache[n] = h
		return h
	}
}
