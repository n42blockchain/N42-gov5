package ethel

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestMakeBlockHashFnWindow pins the EVM BLOCKHASH 256-ancestor window
// semantics of makeBlockHashFn (yellow paper H.2: only ancestors
// currentBlock-1 .. currentBlock-256 resolve; everything else is zero). This
// path was previously untested.
func TestMakeBlockHashFnWindow(t *testing.T) {
	const cur = uint64(1000)
	recent := make([]types.Hash, BlockHashWindowSize) // 256
	for i := range recent {
		recent[i][0] = byte(i)
		recent[i][31] = 0xAB
	}
	fn := makeBlockHashFn(cur, recent)
	base := cur - uint64(len(recent)) // 744 = currentBlock-256

	// In-window boundaries resolve to the right snapshot entry.
	if got := fn(base); got != recent[0] {
		t.Fatalf("deepest in-window block %d: got %x want %x", base, got, recent[0])
	}
	if got := fn(cur - 1); got != recent[len(recent)-1] {
		t.Fatalf("parent block %d: got %x want %x", cur-1, got, recent[len(recent)-1])
	}
	if got := fn(cur - 128); got != recent[128] {
		t.Fatalf("mid-window block %d: got %x", cur-128, got)
	}

	// Out-of-window queries must return the zero hash (NOT a wrong/stale value).
	for _, n := range []uint64{
		base - 1,   // 743: 257-deep → just past the window
		cur,        // current block itself (not an ancestor)
		cur + 1,    // future block
		0,          // genesis, far below window
		cur - 1000, // far below
	} {
		if got := fn(n); got != (types.Hash{}) {
			t.Fatalf("out-of-window block %d: expected zero hash, got %x", n, got)
		}
	}

	// Exactly 256-deep is the deepest valid; 257-deep is zero.
	if fn(cur-256) == (types.Hash{}) {
		t.Fatal("256-deep ancestor must resolve")
	}
	if fn(cur-257) != (types.Hash{}) {
		t.Fatal("257-deep ancestor must be zero")
	}
}

// TestMakeBlockHashFnEmpty: with no recent snapshot, every lookup is zero
// (e.g. genesis-era or a node that hasn't backfilled the window yet).
func TestMakeBlockHashFnEmpty(t *testing.T) {
	fn := makeBlockHashFn(0, nil)
	for _, n := range []uint64{0, 1, 255, 256, 1000} {
		if got := fn(n); got != (types.Hash{}) {
			t.Fatalf("empty window block %d: expected zero, got %x", n, got)
		}
	}
}

func TestMakeRangeBlockHashFnMatchesSnapshot(t *testing.T) {
	const (
		hashStart = uint64(700)
		rangeEnd  = uint64(1100)
	)
	hashes := make([]types.Hash, rangeEnd-hashStart)
	for i := range hashes {
		hashes[i][0] = byte(i)
		hashes[i][30] = byte(i >> 8)
		hashes[i][31] = 0xcd
	}

	for _, current := range []uint64{744, 800, 1000, 1099} {
		fn := makeRangeBlockHashFn(current, hashStart, hashes)
		for n := uint64(0); n < rangeEnd+2; n++ {
			want := types.Hash{}
			if n >= hashStart && n < current && current-n <= BlockHashWindowSize {
				want = hashes[n-hashStart]
			}
			if got := fn(n); got != want {
				t.Fatalf("current=%d n=%d: got %x, want %x", current, n, got, want)
			}
		}
	}
}
