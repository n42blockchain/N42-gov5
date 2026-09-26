// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package txindexer

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/txlookup"
)

// newTestTailWithBlocks builds a Tail holding nBlocks consecutive blocks,
// each with txPerBlock distinct (synthetic) transaction hashes.
func newTestTailWithBlocks(t *testing.T, nBlocks, txPerBlock int) *txlookup.Tail {
	t.Helper()
	tail := txlookup.NewTail()
	for b := 0; b < nBlocks; b++ {
		hashes := make([]types.Hash, txPerBlock)
		for i := range hashes {
			hashes[i][0] = byte(b)
			hashes[i][1] = byte(i)
			hashes[i][2] = byte(i >> 8)
		}
		tail.Add(uint64(b), hashes)
	}
	return tail
}

// TestTxIndexEnvInt covers the shared parser both knobs use: unset, invalid,
// zero/negative all fall back to the compiled default; a valid positive
// integer overrides it.
func TestTxIndexEnvInt(t *testing.T) {
	const name = "N42_TXINDEX_TEST_ENV_INT"
	const def = 64

	cases := []struct {
		v    string
		want int
	}{
		{"", def},
		{"0", def},
		{"-1", def},
		{"bogus", def},
		{"8", 8},
		{"1000000", 1000000},
	}
	for _, c := range cases {
		t.Setenv(name, c.v)
		if got := txIndexEnvInt(name, def); got != c.want {
			t.Errorf("txIndexEnvInt(%q=%q) = %d, want %d", name, c.v, got, c.want)
		}
	}
}

// TestTxIndexKeepBlocksVarDefault confirms the knob is a pure passthrough of
// txIndexKeepBlocks when N42_TXINDEX_KEEP_BLOCKS is unset -- unset must
// behave byte-for-byte as today.
func TestTxIndexKeepBlocksVarDefault(t *testing.T) {
	t.Setenv("N42_TXINDEX_KEEP_BLOCKS", "")
	if got := txIndexKeepBlocksVar(); got != txIndexKeepBlocks {
		t.Fatalf("txIndexKeepBlocksVar() = %d, want the compiled default %d", got, txIndexKeepBlocks)
	}
}

// TestTxIndexKeepBlocksVarOverride confirms N42_TXINDEX_KEEP_BLOCKS actually
// changes the value SealRangeKeepTx is called with.
func TestTxIndexKeepBlocksVarOverride(t *testing.T) {
	t.Setenv("N42_TXINDEX_KEEP_BLOCKS", "8")
	if got := txIndexKeepBlocksVar(); got != 8 {
		t.Fatalf("txIndexKeepBlocksVar() = %d, want 8", got)
	}
}

// TestTxIndexSealMinTxVarDefaultAndOverride mirrors the keep-blocks cases for
// the seal-threshold knob.
func TestTxIndexSealMinTxVarDefaultAndOverride(t *testing.T) {
	t.Setenv("N42_TXINDEX_SEAL_MIN_TX", "")
	if got := txIndexSealMinTxVar(); got != txIndexSealMinTx {
		t.Fatalf("txIndexSealMinTxVar() default = %d, want %d", got, txIndexSealMinTx)
	}
	t.Setenv("N42_TXINDEX_SEAL_MIN_TX", "200000")
	if got := txIndexSealMinTxVar(); got != 200000 {
		t.Fatalf("txIndexSealMinTxVar() override = %d, want 200000", got)
	}
}

// TestSealRangeKeepBlocksInteraction: a smaller keepBlocks widens the
// sealable range SealRangeKeepTx computes -- the interaction the task's own
// N42_TXINDEX_SEAL_MIN_TX knob exists for. Uses txlookup.Tail directly
// (same arithmetic sealTxIndexOnce drives) to prove the effect without
// standing up a full Indexer.
func TestSealRangeKeepBlocksInteraction(t *testing.T) {
	tail := newTestTailWithBlocks(t, 20, 1000) // 20 blocks, 1000 tx each

	// keepBlocks=64 (today's default): every block is "kept," nothing sealable.
	if _, _, ok := tail.SealRangeKeepTx(1_000_000, 256, 64, 1_000_000); ok {
		t.Fatal("keepBlocks=64 over 20 blocks: expected nothing sealable, got ok=true")
	}

	// keepBlocks=8: 12 blocks become sealable (20-8), 12,000 tx -- below the
	// default 1,000,000 seal-min, so still nothing seals...
	if _, _, ok := tail.SealRangeKeepTx(1_000_000, 256, 8, 1_000_000); ok {
		t.Fatal("keepBlocks=8, default seal-min 1,000,000: expected nothing sealable yet, got ok=true")
	}
	// ...but lowering N42_TXINDEX_SEAL_MIN_TX to match the smaller sealable
	// range's own scale (the interaction this task's part (2) is about) lets
	// it seal.
	start, end, ok := tail.SealRangeKeepTx(10_000, 256, 8, 1_000_000)
	if !ok {
		t.Fatal("keepBlocks=8, seal-min 10,000: expected sealable range, got ok=false")
	}
	if start != 0 || end == 0 {
		t.Fatalf("sealable range = [%d,%d), want a non-empty range starting at 0", start, end)
	}
}
