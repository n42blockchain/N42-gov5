// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers several standalone commitment.go helpers left at 0%: Mode.String,
// BranchStat.Collect, DecodeBranchAndCollectStat and BranchData.Validate (both
// exercised against REAL branch data produced by a HexPatriciaHashed Process()
// run, not hand-built bytes), InitializeTrieAndUpdates's three TrieVariant
// branches (including the VariantBinPatriciaTrie panic), the deferred-update
// metrics counter, PendingCommitmentUpdate.Clear, cellFields.String, and
// RetrieveCellNoop/BranchEncoder.SetCache.

package commitment

import (
	"context"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/lib/common/length"
)

func TestModeString(t *testing.T) {
	cases := map[Mode]string{
		ModeDisabled: "disabled",
		ModeDirect:   "direct",
		ModeUpdate:   "update",
		Mode(99):     "unknown",
	}
	for m, want := range cases {
		if got := m.String(); got != want {
			t.Fatalf("Mode(%d).String() = %q, want %q", m, got, want)
		}
	}
}

func TestBranchStatCollect(t *testing.T) {
	bs := &BranchStat{KeySize: 10, ValSize: 20, MinCellSize: 5, MaxCellSize: 50, CellCount: 2}
	other := &BranchStat{KeySize: 3, ValSize: 7, MinCellSize: 2, MaxCellSize: 100, CellCount: 1, MedianExt: 4}
	bs.Collect(other)
	if bs.KeySize != 13 || bs.ValSize != 27 || bs.CellCount != 3 {
		t.Fatalf("Collect sums: KeySize=%d ValSize=%d CellCount=%d", bs.KeySize, bs.ValSize, bs.CellCount)
	}
	if bs.MinCellSize != 2 {
		t.Fatalf("MinCellSize = %d, want 2 (min of 5,2)", bs.MinCellSize)
	}
	if bs.MaxCellSize != 100 {
		t.Fatalf("MaxCellSize = %d, want 100 (max of 50,100)", bs.MaxCellSize)
	}
	// Collect(nil) must be a no-op.
	before := *bs
	bs.Collect(nil)
	if *bs != before {
		t.Fatalf("Collect(nil) must not change the receiver")
	}
}

func TestCellFieldsString(t *testing.T) {
	if got := fieldExtension.String(); got != "DownHash" {
		t.Fatalf("fieldExtension.String() = %q", got)
	}
	combo := (fieldExtension | fieldAccountAddr | fieldHash).String()
	if !strings.Contains(combo, "DownHash") || !strings.Contains(combo, "AccountPlain") || !strings.Contains(combo, "Hash") {
		t.Fatalf("combo String() = %q, missing expected parts", combo)
	}
}

func TestRetrieveCellNoopAndSetCache(t *testing.T) {
	c, err := RetrieveCellNoop(3, false)
	if c != nil || err != nil {
		t.Fatalf("RetrieveCellNoop = (%v,%v), want (nil,nil)", c, err)
	}

	be := &BranchEncoder{}
	be.SetCache(nil) // must not panic
}

func TestDeferredUpdateMetricsAndPendingClear(t *testing.T) {
	ResetDeferredUpdateMetrics()
	if got := GetDeferredUpdateMetrics(); got != 0 {
		t.Fatalf("GetDeferredUpdateMetrics() after reset = %d, want 0", got)
	}
	upd := getDeferredUpdate(nil, 0, 0, 0, &[16]cell{}, 0, nil)
	if got := GetDeferredUpdateMetrics(); got != 1 {
		t.Fatalf("GetDeferredUpdateMetrics() after one get = %d, want 1", got)
	}
	putDeferredUpdate(upd)

	p := &PendingCommitmentUpdate{Deferred: []*DeferredBranchUpdate{
		getDeferredUpdate(nil, 0, 0, 0, &[16]cell{}, 0, nil),
		getDeferredUpdate(nil, 0, 0, 0, &[16]cell{}, 0, nil),
	}}
	p.Clear()
	if p.Deferred != nil {
		t.Fatal("Clear() must nil the Deferred slice")
	}
}

func TestInitializeTrieAndUpdates(t *testing.T) {
	trie, upds := InitializeTrieAndUpdates(VariantHexPatriciaTrie, ModeDirect, t.TempDir())
	if trie == nil || upds == nil {
		t.Fatal("expected non-nil trie and updates for VariantHexPatriciaTrie")
	}

	// Unknown/default variant falls through to the hex-patricia default.
	trie2, upds2 := InitializeTrieAndUpdates(TrieVariant("bogus"), ModeDirect, t.TempDir())
	if trie2 == nil || upds2 == nil {
		t.Fatal("expected non-nil trie and updates for an unrecognized variant (default fallthrough)")
	}

	trie3, upds3 := InitializeTrieAndUpdates(VariantConcurrentHexPatricia, ModeDirect, t.TempDir())
	if trie3 == nil || upds3 == nil {
		t.Fatal("expected non-nil trie and updates for VariantConcurrentHexPatricia")
	}
	if _, ok := trie3.(*ConcurrentPatriciaHashed); !ok {
		t.Fatalf("VariantConcurrentHexPatricia must produce a *ConcurrentPatriciaHashed, got %T", trie3)
	}

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected a panic for VariantBinPatriciaTrie (not implemented)")
		}
	}()
	InitializeTrieAndUpdates(VariantBinPatriciaTrie, ModeDirect, t.TempDir())
}

// buildRealBranches runs a small HexPatriciaHashed update through MockState
// and returns every (prefix, BranchData) pair it produced, for tests that need
// REAL encoded branch data rather than hand-built bytes.
func buildRealBranches(t *testing.T) map[string]BranchData {
	t.Helper()
	ms := NewMockState(t)
	hph := NewHexPatriciaHashed(length.Addr, ms)
	plainKeys, updates := NewUpdateBuilder().
		Balance("0000000000000000000000000000000000000001", 100).
		Balance("0000000000000000000000000000000000000002", 200).
		Balance("0000000000000000000000000000000000000003", 300).
		Balance("00000000000000000000000000000000000000ff", 400).
		Build()
	if err := ms.applyPlainUpdates(plainKeys, updates); err != nil {
		t.Fatal(err)
	}
	upds := WrapKeyUpdates(t, ModeDirect, KeyToHexNibbleHash, plainKeys, updates)
	defer upds.Close()
	if _, err := hph.Process(context.Background(), upds, "", nil, WarmupConfig{}); err != nil {
		t.Fatal(err)
	}
	if len(ms.cm) == 0 {
		t.Fatal("expected at least one branch to be produced by Process()")
	}
	out := make(map[string]BranchData, len(ms.cm))
	for k, v := range ms.cm {
		out[k] = v
	}
	return out
}

func TestBranchDataValidateOnRealBranches(t *testing.T) {
	branches := buildRealBranches(t)
	for prefix, bd := range branches {
		if err := bd.Validate([]byte(prefix)); err != nil {
			t.Fatalf("Validate(prefix=%x) on a real branch failed: %v", prefix, err)
		}
	}

	// Tamper: corrupt the afterMap bits so validateAfterMap's cell-count check
	// fails against the already-decoded cells.
	for prefix, bd := range branches {
		if len(bd) < 4 {
			continue
		}
		tampered := append(BranchData{}, bd...)
		tampered[2] ^= 0xff // flip afterMap high byte
		tampered[3] ^= 0xff
		if err := tampered.Validate([]byte(prefix)); err == nil {
			t.Fatalf("expected Validate to reject a branch with a corrupted afterMap (prefix=%x)", prefix)
		}
		break // one corruption case is enough to prove the error path
	}
}

func TestDecodeBranchAndCollectStatOnRealBranches(t *testing.T) {
	branches := buildRealBranches(t)
	for prefix, bd := range branches {
		stat := DecodeBranchAndCollectStat([]byte(prefix), bd, VariantHexPatriciaTrie)
		if stat == nil {
			t.Fatalf("DecodeBranchAndCollectStat(prefix=%x) returned nil", prefix)
		}
		if stat.KeySize != uint64(len(prefix)) {
			t.Fatalf("stat.KeySize = %d, want %d", stat.KeySize, len(prefix))
		}
		if stat.ValSize != uint64(len(bd)) {
			t.Fatalf("stat.ValSize = %d, want %d", stat.ValSize, len(bd))
		}
	}

	// Empty key must return nil.
	if DecodeBranchAndCollectStat(nil, []byte{1, 2, 3}, VariantHexPatriciaTrie) != nil {
		t.Fatal("expected nil for an empty key")
	}

	// The "state" root key is reported IsRoot=true and skips cell decoding.
	stat := DecodeBranchAndCollectStat([]byte("state"), []byte{1, 2, 3, 4}, VariantHexPatriciaTrie)
	if stat == nil || !stat.IsRoot {
		t.Fatalf("expected IsRoot=true for the \"state\" key, got %+v", stat)
	}
}
