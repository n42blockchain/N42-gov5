package hotstuff

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// TestHotStuff_ConfigAndRewardFunc covers Config() and SetRewardFunc().
func TestHotStuff_ConfigAndRewardFunc(t *testing.T) {
	cfg := &params.HotStuffConfig{BaseTimeout: 1, MaxTimeout: 2, Period: 1}
	h := New(cfg, nil)
	if h.Config() != cfg {
		t.Fatalf("Config(): expected the same config pointer back")
	}
	h.SetRewardFunc(func(*params.ChainConfig, *state.IntraBlockState, *block.Header, consensus.N42ChainHeaderReader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
		return nil, nil, nil
	})
}

// TestHotStuff_EnableH2V4 covers the uninitialized-engine error branch and
// the success branch after InitEngine.
func TestHotStuff_EnableH2V4(t *testing.T) {
	h := New(&params.HotStuffConfig{}, nil)
	if err := h.EnableH2V4(H2V4ChainIdentity{ChainID: 1}); err == nil {
		t.Fatalf("expected error before engine init")
	}

	setup := newTestSetup(t, 4)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	if err := h.EnableH2V4(H2V4ChainIdentity{ChainID: 1}); err != nil {
		t.Fatalf("EnableH2V4 after init: unexpected error: %v", err)
	}
	if !h.Engine().H2V4Enabled() {
		t.Fatalf("expected engine H2V4 enabled after EnableH2V4")
	}
}

// TestHotStuff_IsCurrentLeaderAndOutputCh covers the nil-engine fallback and
// the OutputCh accessor.
func TestHotStuff_IsCurrentLeaderAndOutputCh(t *testing.T) {
	h := New(&params.HotStuffConfig{}, nil)
	if h.IsCurrentLeader() {
		t.Fatalf("expected false before engine init")
	}
	if h.OutputCh() == nil {
		t.Fatalf("expected non-nil output channel")
	}

	setup := newTestSetup(t, 4)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	_ = h.IsCurrentLeader() // exercise the engine-present branch too
}

// TestHotStuff_AuthorAndIsServiceTransaction covers the header-type guard in
// Author and the always-false IsServiceTransaction.
func TestHotStuff_AuthorAndIsServiceTransaction(t *testing.T) {
	h := New(&params.HotStuffConfig{}, nil)

	if _, err := h.Author(nil); err == nil {
		t.Fatalf("expected error for a nil/invalid header type")
	}

	addr := types.Address{0x09}
	hdr := &block.Header{Coinbase: addr}
	got, err := h.Author(hdr)
	if err != nil {
		t.Fatalf("Author: unexpected error: %v", err)
	}
	if got != addr {
		t.Fatalf("Author: expected %x, got %x", addr, got)
	}

	if h.IsServiceTransaction(addr, nil) {
		t.Fatalf("expected IsServiceTransaction to always be false")
	}
}
