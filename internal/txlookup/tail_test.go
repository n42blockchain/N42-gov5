package txlookup

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func tailBlock(num uint64, n int) []types.Hash {
	out := make([]types.Hash, n)
	for i := range out {
		out[i] = hashFor(num, i)
	}
	return out
}

func TestTailLookupAndDrop(t *testing.T) {
	tl := NewTail()
	for n := uint64(10); n < 20; n++ {
		tl.Add(n, tailBlock(n, 4))
	}
	if tl.Len() != 10 || tl.TxCount() != 40 {
		t.Fatalf("tail holds %d blocks / %d txs, want 10/40", tl.Len(), tl.TxCount())
	}
	if got, ok := tl.Lookup(hashFor(13, 2)); !ok || got != 13 {
		t.Fatalf("lookup = %d,%v want 13,true", got, ok)
	}

	tl.DropBelow(15)
	if _, ok := tl.Lookup(hashFor(13, 2)); ok {
		t.Fatal("a dropped block's transaction is still answerable")
	}
	if got, ok := tl.Lookup(hashFor(15, 0)); !ok || got != 15 {
		t.Fatalf("lookup after drop = %d,%v want 15,true", got, ok)
	}
	if first, ok := tl.FirstBlock(); !ok || first != 15 {
		t.Fatalf("firstBlock = %d,%v want 15,true", first, ok)
	}
	if tl.Len() != 5 || tl.TxCount() != 20 {
		t.Fatalf("after drop: %d blocks / %d txs, want 5/20", tl.Len(), tl.TxCount())
	}
}

// TestTailResetsOnGap: the tail's value is that it covers a contiguous range,
// so "not in the tail" can mean "older than the tail". A gap would break that
// and lookups would fall through to segments that do not have the block either.
func TestTailResetsOnGap(t *testing.T) {
	tl := NewTail()
	tl.Add(100, tailBlock(100, 3))
	tl.Add(101, tailBlock(101, 3))
	tl.Add(200, tailBlock(200, 3)) // gap

	if _, ok := tl.Lookup(hashFor(100, 0)); ok {
		t.Fatal("a pre-gap transaction is still answerable; the tail did not reset")
	}
	if first, ok := tl.FirstBlock(); !ok || first != 200 {
		t.Fatalf("firstBlock = %d,%v want 200,true", first, ok)
	}
}

func TestTailSealRange(t *testing.T) {
	tl := NewTail()
	for n := uint64(0); n < 10; n++ {
		tl.Add(n, tailBlock(n, 10)) // 10 txs each
	}
	// Needs 35 txs and must leave 4 blocks: blocks 0..3 give 40 >= 35.
	start, end, ok := tl.SealRange(35, 1000, 4)
	if !ok || start != 0 || end != 4 {
		t.Fatalf("sealRange = %d,%d,%v want 0,4,true", start, end, ok)
	}
	// Not enough sealable transactions once the keep-behind is honoured.
	if _, _, ok := tl.SealRange(1000, 1000, 4); ok {
		t.Fatal("sealRange offered a range that cannot reach minTx")
	}
	if _, _, ok := tl.SealRange(1, 1000, 10); ok {
		t.Fatal("sealRange offered a range that does not leave keepBlocks behind")
	}
}

// TestTailSealsIntoSegmentAndStaysAnswerable is the end-to-end property: a
// block sealed out of the tail is still findable, through the segment.
func TestTailSealsIntoSegmentAndStaysAnswerable(t *testing.T) {
	dir := t.TempDir()
	tl := NewTail()
	blockHashes := map[uint64][]types.Hash{}
	for n := uint64(500); n < 520; n++ {
		hs := tailBlock(n, 8)
		blockHashes[n] = hs
		tl.Add(n, hs)
	}

	start, end, ok := tl.SealRange(64, 1000, 4)
	if !ok {
		t.Fatal("nothing sealable")
	}
	if err := BuildSegmentFromSource(context.Background(), dir, start, end, tl.Source()); err != nil {
		t.Fatalf("seal: %v", err)
	}
	tl.DropBelow(end)

	svc, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	svc.SetVerifier(func(blockNum uint64, txHash types.Hash) (uint64, bool) {
		for i, h := range blockHashes[blockNum] {
			if h == txHash {
				return uint64(i), true
			}
		}
		return 0, false
	})

	for n := uint64(500); n < 520; n++ {
		for i, h := range blockHashes[n] {
			if got, ok := tl.Lookup(h); ok {
				if got != n {
					t.Fatalf("tail: block %d tx %d = %d", n, i, got)
				}
				continue
			}
			got, err := svc.Lookup(nil, h)
			if err != nil {
				t.Fatal(err)
			}
			if got == nil {
				t.Fatalf("block %d tx %d is in neither the tail nor a segment", n, i)
			}
			if *got != n {
				t.Fatalf("segment: block %d tx %d = %d", n, i, *got)
			}
		}
	}
}

// TestTailSealRangeBoundsByBlocks: a transaction threshold alone leaves the
// tail unbounded in blocks when blocks are small, and what a restart costs is
// set by blocks — it re-reads each unsealed one in full to recover its hashes.
func TestTailSealRangeBoundsByBlocks(t *testing.T) {
	tl := NewTail()
	for n := uint64(0); n < 300; n++ {
		tl.Add(n, tailBlock(n, 2)) // 600 txs in 300 blocks
	}
	// Nowhere near 1,000,000 transactions, but well past the block bound.
	start, end, ok := tl.SealRange(1_000_000, 100, 64)
	if !ok {
		t.Fatal("a tail 300 blocks deep offered nothing to seal")
	}
	if start != 0 || end != 100 {
		t.Fatalf("sealRange = %d,%d want 0,100", start, end)
	}
	// The block bound must still honour keepBlocks.
	if _, _, ok := tl.SealRange(1_000_000, 100, 300); ok {
		t.Fatal("the block bound overrode keepBlocks")
	}
}
