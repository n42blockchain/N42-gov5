// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package txindexer

import (
	"context"
	"encoding/binary"
	"runtime"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/txlookup"
)

func boundedTestHash(block uint64, i int) types.Hash {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], block)
	binary.BigEndian.PutUint64(b[8:], uint64(i))
	return crypto.Keccak256Hash(b[:])
}

// TestTailStaysBoundedUnderSteadyCommits commits 2M transactions at a fixed
// rate (10k a block, a seal tick every 5 blocks) through the real sealer and
// asserts the in-memory tail never holds more than the keep-behind plus one
// tick's and one segment's worth, that its heap does not grow with the
// transactions committed, and that a transaction resolves both while it is
// in the tail and after it was sealed into a segment.
func TestTailStaysBoundedUnderSteadyCommits(t *testing.T) {
	if testing.Short() {
		t.Skip("builds ~20 RecSplit segments")
	}
	const (
		txPerBlock = 10_000
		blocks     = 200 // 2M transactions
		tickEvery  = 5
		minTx      = 100_000
		keepBlocks = 8
	)
	t.Setenv("N42_TXINDEX_SEAL_MIN_TX", "100000")
	t.Setenv("N42_TXINDEX_KEEP_BLOCKS", "8")

	dir := t.TempDir()
	svc, err := txlookup.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	x := &Indexer{ctx: context.Background(), txIndexDir: dir, txTail: txlookup.NewTail(), txSegments: svc}
	defer func() {
		x.txIndexMu.Lock()
		if x.txSegments != nil {
			x.txSegments.Close()
		}
		x.txIndexMu.Unlock()
	}()

	// keep-behind + what accumulates below minTx + one tick of commits.
	const bound = keepBlocks*txPerBlock + minTx + tickEvery*txPerBlock
	heapAt := func() uint64 {
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.HeapInuse
	}
	var heapEarly uint64
	maxHeld := 0
	for n := uint64(1); n <= blocks; n++ {
		hashes := make([]types.Hash, txPerBlock)
		for i := range hashes {
			hashes[i] = boundedTestHash(n, i)
		}
		x.Add(n, hashes)
		if got, ok := x.LookupTx(boundedTestHash(n, txPerBlock/2)); !ok || got != n {
			t.Fatalf("block %d: a just-added transaction resolved to %d,%v", n, got, ok)
		}
		if n%tickEvery == 0 {
			for i := 0; i < txIndexSealMaxPerTick && x.sealTxIndexOnce(); i++ {
			}
		}
		if held := x.txTail.TxCount(); held > maxHeld {
			maxHeld = held
		}
		if n == 40 {
			heapEarly = heapAt()
		}
	}
	if maxHeld > bound {
		t.Fatalf("tail held up to %d transactions, bound %d", maxHeld, bound)
	}
	heapLate := heapAt()
	// 160 more blocks = 1.6M transactions committed since the early sample;
	// an unbounded tail would add ~60 MB at its ~37 bytes a transaction.
	if heapLate > heapEarly+16<<20 {
		t.Fatalf("heap grew %d -> %d bytes while the tail should be steady", heapEarly, heapLate)
	}
	first, _ := x.txTail.FirstBlock()
	if first <= 1 {
		t.Fatalf("nothing was sealed: tail starts at %d", first)
	}
	for _, n := range []uint64{first, blocks} {
		if got, ok := x.txTail.Lookup(boundedTestHash(n, 123)); !ok || got != n {
			t.Fatalf("tail: tx of block %d resolved to %d,%v", n, got, ok)
		}
	}
	// Sealed ones through a fresh segment reader (the indexer's own reader
	// confirms candidates against chain blocks this test does not have).
	segs, err := txlookup.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer segs.Close()
	for _, n := range []uint64{1, 77, first - 1} {
		got, err := segs.Lookup(nil, boundedTestHash(n, 123))
		if err != nil || got == nil || *got != n {
			t.Fatalf("segments: tx of block %d resolved to %v,%v (tail starts at %d)", n, got, err, first)
		}
	}
	t.Logf("max tail %d txs (bound %d), tail now %d blocks from %d, heap %d -> %d MB",
		maxHeld, bound, x.txTail.Len(), first, heapEarly>>20, heapLate>>20)
}
