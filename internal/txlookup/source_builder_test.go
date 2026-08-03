package txlookup

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// memSource builds a BlockTxSource over per-block hash lists starting at
// startBlock, with a deterministic hash per (block, index).
func memSource(startBlock uint64, perBlock [][]types.Hash) BlockTxSource {
	return func(n uint64) ([]types.Hash, error) {
		i := int(n - startBlock)
		if i < 0 || i >= len(perBlock) {
			return nil, nil
		}
		return perBlock[i], nil
	}
}

func hashFor(blockNum uint64, idx int) types.Hash {
	var h types.Hash
	h[0] = byte(blockNum)
	h[1] = byte(blockNum >> 8)
	h[2] = byte(idx)
	h[3] = byte(idx >> 8)
	// Spread the rest so keys are not clustered by construction; RecSplit is
	// insensitive to this but a clustered set would make the test weaker than
	// the real key distribution.
	h[31] = byte(blockNum*31 + uint64(idx)*17)
	return h
}

// TestBuildSegmentFromSourceResolvesEveryTransaction is the property the whole
// tier rests on: every transaction the segment was built from resolves to the
// block it was in.
func TestBuildSegmentFromSourceResolvesEveryTransaction(t *testing.T) {
	dir := t.TempDir()
	const startBlock, blocks = 5_000, 40

	perBlock := make([][]types.Hash, blocks)
	want := map[types.Hash]uint64{}
	for b := 0; b < blocks; b++ {
		// Uneven block sizes, including empty ones: the Elias-Fano block
		// boundaries are what map an ordinal back to a block, and a uniform
		// fixture would not exercise them.
		n := (b * 7) % 23
		num := uint64(startBlock + b)
		for i := 0; i < n; i++ {
			h := hashFor(num, i)
			perBlock[b] = append(perBlock[b], h)
			want[h] = num
		}
	}

	if err := BuildSegmentFromSource(context.Background(), dir,
		startBlock, startBlock+blocks, memSource(startBlock, perBlock)); err != nil {
		t.Fatalf("build: %v", err)
	}

	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("open service: %v", err)
	}
	defer svc.Close()
	if svc.SegmentCount() != 1 {
		t.Fatalf("segment count = %d, want 1", svc.SegmentCount())
	}

	for h, wantBlock := range want {
		got, err := svc.Lookup(nil, h)
		if err != nil {
			t.Fatalf("lookup %s: %v", h, err)
		}
		if got == nil {
			t.Fatalf("lookup %s: not found, want block %d", h, wantBlock)
		}
		if *got != wantBlock {
			t.Fatalf("lookup %s = block %d, want %d", h, *got, wantBlock)
		}
	}
}

// TestBuildSegmentFromSourceRejectsUnknownHash: the fingerprint is what keeps a
// segment from answering for a transaction it does not hold. Without it a
// multi-segment store returns the wrong block rather than "not found".
func TestBuildSegmentFromSourceRejectsUnknownHash(t *testing.T) {
	dir := t.TempDir()
	const startBlock, blocks = 100, 8
	perBlock := make([][]types.Hash, blocks)
	for b := 0; b < blocks; b++ {
		for i := 0; i < 16; i++ {
			perBlock[b] = append(perBlock[b], hashFor(uint64(startBlock+b), i))
		}
	}
	if err := BuildSegmentFromSource(context.Background(), dir,
		startBlock, startBlock+blocks, memSource(startBlock, perBlock)); err != nil {
		t.Fatalf("build: %v", err)
	}
	svc, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	misses := 0
	for i := 0; i < 512; i++ {
		var h types.Hash
		h[0], h[1], h[30], h[31] = 0xff, byte(i), byte(i>>8), 0xff
		got, err := svc.Lookup(nil, h)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			misses++
		}
	}
	// The fingerprint is 8-bit, so a small false-positive rate is expected by
	// design; what must not happen is most unknown hashes resolving.
	if misses < 480 {
		t.Fatalf("only %d/512 unknown hashes reported not-found; the existence "+
			"fingerprint is not doing its job", misses)
	}
}

// TestVariableWidthSegmentsResolve is why the ranges file exists: segments
// sized by transaction count are not all SegmentSize wide, so a reader that
// derives a segment's start block from its number resolves every hash to the
// wrong block.
func TestVariableWidthSegmentsResolve(t *testing.T) {
	dir := t.TempDir()
	want := map[types.Hash]uint64{}
	blockHashes := map[uint64][]types.Hash{}

	build := func(start, blocks uint64, txPerBlock int) {
		perBlock := make([][]types.Hash, blocks)
		for b := uint64(0); b < blocks; b++ {
			num := start + b
			for i := 0; i < txPerBlock; i++ {
				h := hashFor(num, i)
				perBlock[b] = append(perBlock[b], h)
				want[h] = num
				blockHashes[num] = append(blockHashes[num], h)
			}
		}
		if err := BuildSegmentFromSource(context.Background(), dir,
			start, start+blocks, memSource(start, perBlock)); err != nil {
			t.Fatalf("build %d+%d: %v", start, blocks, err)
		}
	}
	// Three segments of different widths, contiguous, as a transaction-count
	// sealer would produce them.
	build(1_000, 12, 40)
	build(1_012, 5, 96)
	build(1_017, 30, 16)

	svc, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if svc.SegmentCount() != 3 {
		t.Fatalf("segment count = %d, want 3", svc.SegmentCount())
	}
	// A verifier is mandatory once there is more than one segment. The
	// existence fingerprint is 8 bits, so roughly one out-of-set hash in 256
	// still resolves in a segment that does not hold it, and the newest-first
	// probe then returns that segment's answer. Without this the test below
	// fails on a handful of keys -- which is the same wrong block an RPC would
	// return.
	svc.SetVerifier(func(blockNum uint64, txHash types.Hash) (uint64, bool) {
		for i, h := range blockHashes[blockNum] {
			if h == txHash {
				return uint64(i), true
			}
		}
		return 0, false
	})
	for h, wantBlock := range want {
		got, err := svc.Lookup(nil, h)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Fatalf("lookup %s: not found, want block %d", h, wantBlock)
		}
		if *got != wantBlock {
			t.Fatalf("lookup %s = block %d, want %d", h, *got, wantBlock)
		}
	}
}

// TestMultiSegmentLookupNeedsVerifier pins the requirement rather than leaving
// it to a comment: with more than one segment and no verifier, the newest-first
// probe returns a wrong block for some transactions.
//
// The builder's doc calls LessFalsePositives "required for correct
// multi-segment lookup", which reads as though it were sufficient. It is not:
// the fingerprint is 8 bits, so roughly one out-of-set hash in 256 still
// resolves in a segment that does not hold it, and the probe stops there. Over
// a few thousand keys that is several silently wrong answers -- an RPC
// reporting a transaction in the wrong block.
func TestMultiSegmentLookupNeedsVerifier(t *testing.T) {
	dir := t.TempDir()
	want := map[types.Hash]uint64{}
	blockHashes := map[uint64][]types.Hash{}

	build := func(start, blocks uint64, txPerBlock int) {
		perBlock := make([][]types.Hash, blocks)
		for b := uint64(0); b < blocks; b++ {
			num := start + b
			for i := 0; i < txPerBlock; i++ {
				h := hashFor(num, i)
				perBlock[b] = append(perBlock[b], h)
				want[h] = num
				blockHashes[num] = append(blockHashes[num], h)
			}
		}
		if err := BuildSegmentFromSource(context.Background(), dir,
			start, start+blocks, memSource(start, perBlock)); err != nil {
			t.Fatalf("build: %v", err)
		}
	}
	build(2_000, 40, 64)
	build(2_040, 40, 64)
	build(2_080, 40, 64)

	svc, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	countWrong := func() int {
		wrong := 0
		for h, wantBlock := range want {
			got, err := svc.Lookup(nil, h)
			if err != nil {
				t.Fatal(err)
			}
			if got == nil || *got != wantBlock {
				wrong++
			}
		}
		return wrong
	}

	if unverified := countWrong(); unverified == 0 {
		t.Skip("no phantom hit landed in this fixture; the property still holds " +
			"statistically but this run cannot demonstrate it")
	} else {
		t.Logf("without a verifier: %d of %d transactions resolved to the wrong block",
			unverified, len(want))
	}

	svc.SetVerifier(func(blockNum uint64, txHash types.Hash) (uint64, bool) {
		for i, h := range blockHashes[blockNum] {
			if h == txHash {
				return uint64(i), true
			}
		}
		return 0, false
	})
	if wrong := countWrong(); wrong != 0 {
		t.Fatalf("with a verifier: %d transactions still resolved to the wrong block", wrong)
	}
}
