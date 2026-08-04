package txlookup

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// tieredLookup mirrors what BlockChain.LookupTx does: the tail first, then the
// segments. It is here rather than in the root package because that package
// needs a whole chain to construct, and the property being tested belongs to
// these two tiers.
func tieredLookup(tl *Tail, svc *Service, h types.Hash) (uint64, bool) {
	if n, ok := tl.Lookup(h); ok {
		return n, true
	}
	n, err := svc.Lookup(nil, h)
	if err != nil || n == nil {
		return 0, false
	}
	return *n, true
}

// TestTieredLookupCoversEveryBlockAcrossSeals is the property the whole change
// rests on: at no point does a committed transaction stop being findable.
//
// Blocks arrive, the tail seals its oldest range into a segment and drops it,
// more blocks arrive, it seals again. After each step every transaction from
// every block committed so far must still resolve to its own block — through
// whichever tier now holds it.
func TestTieredLookupCoversEveryBlockAcrossSeals(t *testing.T) {
	dir := t.TempDir()
	tl := NewTail()
	blockHashes := map[uint64][]types.Hash{}

	svc, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Closes whichever service is current: seal() replaces it, and a deferred
	// svc.Close() would capture the first one — closing it twice and leaving
	// the last one open, which on Windows blocks TempDir cleanup.
	defer func() { svc.Close() }()
	verifier := func(blockNum uint64, txHash types.Hash) (uint64, bool) {
		for i, h := range blockHashes[blockNum] {
			if h == txHash {
				return uint64(i), true
			}
		}
		return 0, false
	}
	svc.SetVerifier(verifier)

	const first = 900
	next := uint64(first)
	addBlocks := func(n int) {
		for i := 0; i < n; i++ {
			hs := tailBlock(next, 12)
			blockHashes[next] = hs
			tl.Add(next, hs)
			next++
		}
	}
	checkAll := func(stage string) {
		t.Helper()
		for num := uint64(first); num < next; num++ {
			for i, h := range blockHashes[num] {
				got, ok := tieredLookup(tl, svc, h)
				if !ok {
					t.Fatalf("%s: block %d tx %d findable in no tier", stage, num, i)
				}
				if got != num {
					t.Fatalf("%s: block %d tx %d resolved to %d", stage, num, i, got)
				}
			}
		}
	}

	seal := func(stage string) {
		start, end, ok := tl.SealRange(60, 1000, 8)
		if !ok {
			t.Fatalf("%s: nothing sealable", stage)
		}
		if err := BuildSegmentFromSource(context.Background(), dir, start, end, tl.Source()); err != nil {
			t.Fatalf("%s: seal: %v", stage, err)
		}
		// Sealed, then reopened, then dropped — never dropped before the
		// segment that replaces it can be read.
		reopened, err := NewService(dir)
		if err != nil {
			t.Fatalf("%s: reopen: %v", stage, err)
		}
		reopened.SetVerifier(verifier)
		svc.Close()
		svc = reopened
		tl.DropBelow(end)
	}

	addBlocks(20)
	checkAll("after first blocks")
	seal("first seal")
	checkAll("after first seal")
	addBlocks(20)
	checkAll("after more blocks")
	seal("second seal")
	checkAll("after second seal")

	if svc.SegmentCount() != 2 {
		t.Fatalf("segment count = %d, want 2", svc.SegmentCount())
	}
	if _, ok := tl.Lookup(blockHashes[first][0]); ok {
		t.Fatal("the first block is still in the tail; sealing did not drop it")
	}
}

// TestRebuildFromSourceRestoresTail models a restart: the tail is gone, and is
// refilled from blocks that are already durable, starting where the segments
// end.
func TestRebuildFromSourceRestoresTail(t *testing.T) {
	dir := t.TempDir()
	blockHashes := map[uint64][]types.Hash{}
	for n := uint64(300); n < 340; n++ {
		blockHashes[n] = tailBlock(n, 10)
	}
	src := func(n uint64) ([]types.Hash, error) { return blockHashes[n], nil }

	if err := BuildSegmentFromSource(context.Background(), dir, 300, 320, src); err != nil {
		t.Fatal(err)
	}
	if got := SealedEnd(dir); got != 320 {
		t.Fatalf("SealedEnd = %d, want 320", got)
	}

	// Restart: rebuild only what the segments do not cover.
	tl := NewTail()
	for n := SealedEnd(dir); n < 340; n++ {
		hs, _ := src(n)
		tl.Add(n, hs)
	}
	if tl.Len() != 20 {
		t.Fatalf("rebuilt tail holds %d blocks, want 20", tl.Len())
	}

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

	for n := uint64(300); n < 340; n++ {
		for i, h := range blockHashes[n] {
			got, ok := tieredLookup(tl, svc, h)
			if !ok || got != n {
				t.Fatalf("after restart: block %d tx %d = %d,%v", n, i, got, ok)
			}
		}
	}
}
