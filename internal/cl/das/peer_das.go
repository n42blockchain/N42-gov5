// Copyright 2024 The Erigon Authors / 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Phase 7.2.0 — minimal-stub PeerDas implementation for N42.
//
// Background: ../erigon/cl/das/peer_das.go is 1,155 lines deep into the
// erigon networking stack (gossip, sentinel, p2p, persistence/blob_storage,
// freezeblocks, rpc/eth_clock). Cherry-picking the full thing pulls 10+
// missing subpackages and is multi-week work — see
// docs/ethel/caplin-phase-7-plan.md.
//
// What this file provides instead: the PeerDas interface signature (so
// the Phase 7.2 forkchoice cherry-pick can satisfy its single use site
// `f.peerDas das.PeerDas`) plus a NoopPeerDas implementation that
// returns safe-default values. Mainnet pre-Fusaka does not exercise
// PeerDAS; the noop covers that surface. Real implementation lands
// when Fusaka activates AND N42 wires the network stack.

//go:build n42el

package das

import (
	"context"
	"errors"

	"github.com/n42blockchain/N42/internal/cl/cltypes"
	peerdasstate "github.com/n42blockchain/N42/internal/cl/das/state"
	depcommon "github.com/n42blockchain/N42/internal/cl/depshim/common"
)

// BlockGetter is the minimum shape the PeerDas implementation needs
// from the fork-choice store. Declared here to avoid an import cycle
// when forkchoice depends on das and das wants block lookups.
// [New in Gloas:EIP7732] in upstream — included for signature parity.
type BlockGetter interface {
	GetBlock(blockRoot depcommon.Hash) (*cltypes.SignedBeaconBlock, bool)
}

// PeerDas mirrors ../erigon/cl/das.PeerDas one-for-one so the
// forkchoice cherry-pick compiles unchanged. Method bodies live in
// NoopPeerDas below for the follower-mode N42 deployment.
type PeerDas interface {
	DownloadColumnsAndRecoverBlobs(ctx context.Context, blocks []cltypes.ColumnSyncableSignedBlock) error
	DownloadOnlyCustodyColumns(ctx context.Context, blocks []cltypes.ColumnSyncableSignedBlock) error
	IsDataAvailable(slot uint64, blockRoot depcommon.Hash) (bool, error)
	Prune(keepSlotDistance uint64) error
	UpdateValidatorsCustody(cgc uint64)
	TryScheduleRecover(slot uint64, blockRoot depcommon.Hash) error
	IsBlobAlreadyRecovered(blockRoot depcommon.Hash) bool
	IsColumnOverHalf(slot uint64, blockRoot depcommon.Hash) bool
	IsArchivedMode() bool
	StateReader() peerdasstate.PeerDasStateReader
	SyncColumnDataLater(block *cltypes.SignedBeaconBlock) error
	SetForkChoice(forkChoice BlockGetter)
}

// errPeerDASNotWired is returned by every fallible NoopPeerDas method
// so callers see an explicit "Phase 7.2+ work pending" rather than a
// silent success that would corrupt PeerDAS-dependent attestation
// scoring.
var errPeerDASNotWired = errors.New(
	"das: PeerDAS not wired (Phase 7.2 placeholder; see caplin-phase-7-plan.md)")

// NoopPeerDas satisfies PeerDas with safe defaults so the forkchoice
// store can call its methods on pre-Fusaka mainnet without crashing.
// Every method either returns errPeerDASNotWired or a "no data" answer
// that the caller treats as "not yet relevant".
type NoopPeerDas struct {
	state peerdasstate.PeerDasStateReader
}

// NewNoopPeerDas returns a NoopPeerDas wrapped around the given
// state reader (or a panic-on-call zero state if state is nil).
func NewNoopPeerDas(state peerdasstate.PeerDasStateReader) *NoopPeerDas {
	return &NoopPeerDas{state: state}
}

// Compile-time check that NoopPeerDas satisfies the PeerDas interface.
var _ PeerDas = (*NoopPeerDas)(nil)

func (n *NoopPeerDas) DownloadColumnsAndRecoverBlobs(_ context.Context, _ []cltypes.ColumnSyncableSignedBlock) error {
	return errPeerDASNotWired
}

func (n *NoopPeerDas) DownloadOnlyCustodyColumns(_ context.Context, _ []cltypes.ColumnSyncableSignedBlock) error {
	return errPeerDASNotWired
}

// IsDataAvailable returns (true, nil) so pre-Fusaka mainnet blocks
// (which carry no DAS data) flow through the fork-choice store
// without rejection. Post-Fusaka, the real PeerDas implementation
// must replace this — otherwise the fork-choice will accept
// unavailable blocks.
func (n *NoopPeerDas) IsDataAvailable(_ uint64, _ depcommon.Hash) (bool, error) {
	return true, nil
}

func (n *NoopPeerDas) Prune(_ uint64) error { return nil }

func (n *NoopPeerDas) UpdateValidatorsCustody(_ uint64) {}

func (n *NoopPeerDas) TryScheduleRecover(_ uint64, _ depcommon.Hash) error {
	return errPeerDASNotWired
}

func (n *NoopPeerDas) IsBlobAlreadyRecovered(_ depcommon.Hash) bool { return false }

func (n *NoopPeerDas) IsColumnOverHalf(_ uint64, _ depcommon.Hash) bool { return false }

func (n *NoopPeerDas) IsArchivedMode() bool { return false }

func (n *NoopPeerDas) StateReader() peerdasstate.PeerDasStateReader { return n.state }

func (n *NoopPeerDas) SyncColumnDataLater(_ *cltypes.SignedBeaconBlock) error {
	return errPeerDASNotWired
}

func (n *NoopPeerDas) SetForkChoice(_ BlockGetter) {}
