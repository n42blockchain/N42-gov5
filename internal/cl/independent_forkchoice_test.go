//go:build n42el

// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// Deterministic guard for the B+ adversarial-environment guarantee (#34): the
// EL head must be sourced ENTIRELY from the attestation-weighted fork choice and
// NEVER from the EL's own (potentially attacker-controlled) devp2p tip.
//
// resolveHeadExec takes only a headResolver (the fork-choice store) — it has no
// access to the engine / EL tip at all — so the property is enforced
// structurally: a malicious EL peer feeding a valid-but-non-canonical chain
// cannot influence what head we drive the EL to. These tests pin that the
// resolved head is the fork-choice head's execution hash, and that we refuse to
// drive when the head's payload is unknown.

package cl

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes/solid"
	common "github.com/n42blockchain/N42/internal/cl/depshim/common"
	state2 "github.com/n42blockchain/N42/internal/cl/phase1/core/state"
)

// fakeHeadResolver implements headResolver with no EL-tip notion whatsoever.
type fakeHeadResolver struct {
	headRoot      common.Hash
	headSlot      uint64
	headErr       error
	eth1          map[common.Hash]common.Hash
	finalizedRoot common.Hash
	finalizedExec common.Hash
}

func (f *fakeHeadResolver) GetHead(*state2.CachingBeaconState) (common.Hash, uint64, error) {
	return f.headRoot, f.headSlot, f.headErr
}
func (f *fakeHeadResolver) GetEth1Hash(r common.Hash) common.Hash { return f.eth1[r] }
func (f *fakeHeadResolver) GetFinalizedExecutionHash(r common.Hash) common.Hash {
	if r == f.finalizedRoot {
		return f.finalizedExec
	}
	return common.Hash{}
}
func (f *fakeHeadResolver) FinalizedCheckpoint() solid.Checkpoint {
	return solid.Checkpoint{Root: f.finalizedRoot, Epoch: 0}
}

func testBeaconConfig(t *testing.T) *clparams.BeaconChainConfig {
	t.Helper()
	_, beaconCfg, _, err := clparams.GetConfigsByNetworkName("mainnet")
	if err != nil {
		t.Fatalf("GetConfigsByNetworkName(mainnet): %v", err)
	}
	return beaconCfg
}

// TestResolveHeadExec_SourcesHeadFromForkChoice asserts the EL head is the
// fork-choice head's execution hash — proving the head comes from
// attestation-weighted fork choice, not any EL tip.
func TestResolveHeadExec_SourcesHeadFromForkChoice(t *testing.T) {
	cfg := testBeaconConfig(t)

	headRoot := common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	fcHeadExec := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	finRoot := common.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	finExec := common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")

	r := &fakeHeadResolver{
		headRoot:      headRoot,
		headSlot:      64,
		eth1:          map[common.Hash]common.Hash{headRoot: fcHeadExec},
		finalizedRoot: finRoot,
		finalizedExec: finExec,
	}

	finalizedExec, headExec, _, ok, err := resolveHeadExec(r, cfg)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true when head payload is known")
	}
	if headExec != fcHeadExec {
		t.Fatalf("head must be the fork-choice head exec hash: got %x want %x", headExec, fcHeadExec)
	}
	if finalizedExec != finExec {
		t.Fatalf("finalized must be the fork-choice finalized exec hash: got %x want %x", finalizedExec, finExec)
	}
}

// TestResolveHeadExec_FinalizedZeroWhenUnknown asserts that when the finalized
// execution hash is unknown, finalized is left ZERO (read by the Engine API as
// "no finalized block") and is NEVER substituted with head — substituting head
// would assert a still-reorg-able block as finalized. Head still drives.
func TestResolveHeadExec_FinalizedZeroWhenUnknown(t *testing.T) {
	cfg := testBeaconConfig(t)
	headRoot := common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	fcHeadExec := common.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")
	r := &fakeHeadResolver{
		headRoot:      headRoot,
		headSlot:      0,
		eth1:          map[common.Hash]common.Hash{headRoot: fcHeadExec},
		finalizedRoot: common.HexToHash("0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"),
		// finalizedExec left zero → GetFinalizedExecutionHash returns zero
	}
	finalizedExec, headExec, _, ok, err := resolveHeadExec(r, cfg)
	if err != nil || !ok {
		t.Fatalf("unexpected err=%v ok=%v", err, ok)
	}
	if headExec != fcHeadExec {
		t.Fatalf("head must still be the fork-choice head exec: got %x want %x", headExec, fcHeadExec)
	}
	if finalizedExec != (common.Hash{}) {
		t.Fatalf("finalized must be zero when unknown (never head): got %x", finalizedExec)
	}
}

// TestResolveHeadExec_SkipsWhenPayloadUnknown asserts we refuse to drive the EL
// when the fork-choice head's execution payload is not yet known (ok=false) —
// we never substitute the EL's own tip.
func TestResolveHeadExec_SkipsWhenPayloadUnknown(t *testing.T) {
	cfg := testBeaconConfig(t)
	r := &fakeHeadResolver{
		headRoot: common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		eth1:     map[common.Hash]common.Hash{}, // no entry → zero exec hash
	}
	_, _, _, ok, err := resolveHeadExec(r, cfg)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false when head payload unknown (must not drive EL)")
	}
}

// TestResolveHeadExec_PropagatesGetHeadError asserts a fork-choice GetHead error
// is surfaced (no EL drive).
func TestResolveHeadExec_PropagatesGetHeadError(t *testing.T) {
	cfg := testBeaconConfig(t)
	r := &fakeHeadResolver{headErr: errors.New("justified root not resolvable yet")}
	_, _, _, ok, err := resolveHeadExec(r, cfg)
	if err == nil {
		t.Fatal("expected GetHead error to be propagated")
	}
	if ok {
		t.Fatal("expected ok=false on GetHead error")
	}
}
