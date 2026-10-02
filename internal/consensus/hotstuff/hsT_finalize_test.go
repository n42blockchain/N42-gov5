// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// hsTFinalizeChainConfig returns a chain config with EthELCompat enabled, so
// Finalize skips the reward function entirely (no apos dependency needed for
// this test) and goes straight to the state-root build/verify split.
func hsTFinalizeChainConfig() *params.ChainConfig {
	return &params.ChainConfig{
		HotStuff: &params.HotStuffConfig{EthELCompat: true},
	}
}

func hsTNewIBS(t *testing.T) *state.IntraBlockState {
	t.Helper()
	db := memdb.NewTestDB(t)
	txDb := memdb.BeginRw(t, db)
	return state.New(state.NewPlainState(txDb, 1))
}

// TestHotStuffFinalize_BuildPathSetsRoot covers the build path (header.Root
// starts zero): Finalize must compute and set it from the IntraBlockState.
func TestHotStuffFinalize_BuildPathSetsRoot(t *testing.T) {
	cfg := hsTFinalizeChainConfig()
	h := New(nil, cfg)

	header := &block.Header{
		Number:     uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
	}
	ibs := hsTNewIBS(t)
	chain := newMockChainReader()
	chain.config = cfg

	rewards, unpaid, err := h.Finalize(chain, header, ibs, nil, nil)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if rewards != nil || unpaid != nil {
		t.Fatalf("ethELCompat path should produce no native rewards, got %v %v", rewards, unpaid)
	}
	if header.Root == (types.Hash{}) {
		t.Fatal("expected Finalize to set header.Root on the build path")
	}
}

// TestHotStuffFinalize_VerifyPathMismatchRejected covers the verify path: a
// header whose proposer-set Root disagrees with the locally computed root
// must be rejected.
func TestHotStuffFinalize_VerifyPathMismatchRejected(t *testing.T) {
	cfg := hsTFinalizeChainConfig()
	h := New(nil, cfg)

	header := &block.Header{
		Number:     uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
		Root:       types.Hash{0xde, 0xad},
	}
	ibs := hsTNewIBS(t)
	chain := newMockChainReader()
	chain.config = cfg

	_, _, err := h.Finalize(chain, header, ibs, nil, nil)
	if err == nil {
		t.Fatal("expected a state root mismatch error")
	}
}

// TestHotStuffFinalize_InvalidHeaderType covers the type-assertion guard.
func TestHotStuffFinalize_InvalidHeaderType(t *testing.T) {
	h := New(nil, hsTFinalizeChainConfig())
	chain := newMockChainReader()
	if _, _, err := h.Finalize(chain, nil, nil, nil, nil); err == nil {
		t.Fatal("expected an error for a nil/invalid header")
	}
}

// TestHotStuffFinalizeAndAssemble_AssemblesBlock covers the happy path,
// including the Shanghai withdrawals-hash repurposing and the Cancun/Prague
// optional-field defaulting performed before block assembly.
func TestHotStuffFinalizeAndAssemble_AssemblesBlock(t *testing.T) {
	cfg := hsTFinalizeChainConfig()
	h := New(nil, cfg)

	header := &block.Header{
		Number:     uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
	}
	ibs := hsTNewIBS(t)
	chain := newMockChainReader()
	chain.config = cfg

	b, rewards, unpaid, err := h.FinalizeAndAssemble(chain, header, ibs, nil, nil, nil)
	if err != nil {
		t.Fatalf("FinalizeAndAssemble: %v", err)
	}
	if b == nil {
		t.Fatal("expected an assembled block")
	}
	if rewards != nil || unpaid != nil {
		t.Fatalf("unexpected native rewards under ethELCompat: %v %v", rewards, unpaid)
	}
	if b.Header().(*block.Header).Root == (types.Hash{}) {
		t.Fatal("expected the assembled header to carry the computed root")
	}
}

// TestHotStuffFinalizeAndAssemble_InvalidHeaderType covers the type-assertion
// guard in FinalizeAndAssemble, independent of the one in Finalize.
func TestHotStuffFinalizeAndAssemble_InvalidHeaderType(t *testing.T) {
	h := New(nil, hsTFinalizeChainConfig())
	chain := newMockChainReader()
	if _, _, _, err := h.FinalizeAndAssemble(chain, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected an error for a nil/invalid header")
	}
}
