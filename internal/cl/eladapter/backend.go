// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Backend unit for the eladapter package.
// Defines the Backend and Adapter types.
// Provides constructors New and NewPayload.
// Exports helpers such as NewPayload, ForkChoiceUpdate, SupportInsertion,
// and InsertBlocks.
// Execution-layer adapter bridging CL and EL.

//go:build n42el

// Package eladapter implements Caplin's
// internal/cl/phase1/execution_client.ExecutionEngine interface against
// N42's n42-el execution layer.
//
// Caplin code calls into the 14-method ExecutionEngine; this package
// satisfies that interface and delegates the operations it can already
// answer to a narrow Backend (provided by cmd/ethexec/beacon_backend.go).
// Methods that need a live Engine API server still return
// ErrNotImplemented — see PHASE6_NOTES.md for the architectural seam.

package eladapter

import (
	"context"
	"errors"
	"math/big"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	depcommon "github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/engineapi/engine_types"
	"github.com/n42blockchain/N42/internal/cl/depshim/hexutil"
	deptypes "github.com/n42blockchain/N42/internal/cl/depshim/types"
	"github.com/n42blockchain/N42/internal/cl/depshim/typesproto"
	"github.com/n42blockchain/N42/internal/cl/phase1/execution_client"
)

// ErrNotImplemented is returned by stub methods that have the right
// signature but no real body yet. See PHASE6_NOTES.md.
var ErrNotImplemented = errors.New("eladapter: not implemented")

// Backend is the narrow surface eladapter needs from the surrounding
// n42-el runtime. cmd/eth-el/beacon_backend.go provides the
// implementation. Methods are invoked from Caplin's stage loop and must
// be safe for concurrent use.
type Backend interface {
	CurrentHeadNumber(ctx context.Context) (uint64, error)
	HasBlock(ctx context.Context, hash depcommon.Hash) (bool, error)
	IsCanonicalHash(ctx context.Context, hash depcommon.Hash) (bool, error)
	Ready(ctx context.Context) (bool, error)

	// Phase 7.1.1 — Engine API surface. The Backend owns the
	// *api.EngineStateAdapter (real EVM execute + state root verify +
	// MDBX commit) and is the only place cltypes.Eth1Block →
	// common/block.Block conversion happens. eladapter passes Caplin's
	// payload as-is and lets the Backend do the type translation.
	ExecutePayload(
		ctx context.Context,
		blk *cltypes.Eth1Block,
		parentBeaconBlockRoot *depcommon.Hash,
		versionedHashes []depcommon.Hash,
		executionRequests []hexutil.Bytes,
	) (execution_client.PayloadStatus, error)

	UpdateForkchoice(
		ctx context.Context,
		head, safe, finalized depcommon.Hash,
		attrs *engine_types.PayloadAttributes,
		version clparams.StateVersion,
	) (payloadID []byte, err error)

	ReadCurrentHeader(ctx context.Context) (*deptypes.Header, error)

	// Phase 7.1.2 — Historical import path. Caplin's block_collector
	// calls InsertBlock(s) to push backfill blocks into the EL.
	// Backend chooses how to materialise: erigon-style pure storage
	// (writes block to chaindata, defers execution to a stage loop),
	// or N42's NewPayload-loop variant (full Engine API validation +
	// EVM + state-root verify per block, single-step).
	InsertBlock(ctx context.Context, blk *deptypes.Block) error
	InsertBlocks(ctx context.Context, blks []*deptypes.Block, wait bool) error

	// Phase 7.1.3 — Body read path. Caplin pulls block bodies from
	// EL via these two methods (mostly for RPC eth_getBlockByXXX
	// serving, occasionally for re-org body reconstruction).
	//
	// N42's common/block.Body does not store Withdrawals (post-Shanghai
	// withdrawals are applied directly to IntraBlockState by the
	// EngineStateAdapter and never persisted to body), so RawBody
	// returned here has Withdrawals=nil. Downstream RPC handlers
	// that need withdrawals must reconstruct from changesets — a
	// follow-up beyond Phase 7.1.
	GetBodiesByRange(ctx context.Context, start, count uint64) ([]*deptypes.RawBody, error)
	GetBodiesByHashes(ctx context.Context, hashes []depcommon.Hash) ([]*deptypes.RawBody, error)

	// Phase 7.1.4 — Block production path. Caplin in proposer role
	// calls GetAssembledBlock to fetch an EL-assembled payload + blobs
	// bundle + execution-requests bundle + block value. Required
	// only when the local node is a mainnet validator AND the slot
	// is its propose slot.
	//
	// N42 eth-el currently runs in follower mode (no validator key
	// management, no attestation upload). The Backend may return
	// errBlockProductionNotWired / ErrNotImplemented; Caplin's stage
	// loop falls through to a non-proposer path. Wiring this to
	// EngineAPIBlob.GetPayloadV3 + a builder is Phase 7.4 work.
	GetAssembledBlock(
		ctx context.Context,
		idBytes []byte,
		version clparams.StateVersion,
	) (*cltypes.Eth1Block, *engine_types.BlobsBundle, *typesproto.RequestsBundle, *big.Int, error)

	// Phase 7.1.5 — Blob retrieval (EIP-4844). Caplin queries this
	// when validating attestations that reference blob commitments.
	// Backed by an EL-side blob store (mempool blob TX cache +
	// recent-blocks persisted blobs). N42's eth-el doesn't maintain
	// a blob store at the moment — the Backend may return empty
	// slices; this is acceptable for follower nodes that don't
	// serve blob attestations.
	GetBlobs(
		ctx context.Context,
		versionedHashes []depcommon.Hash,
		version clparams.StateVersion,
	) (blobs [][]byte, proofs [][][]byte, err error)
}

// Adapter implements Caplin's ExecutionEngine interface by delegating
// every non-stub call to the configured Backend.
type Adapter struct {
	backend Backend
}

// New constructs an Adapter around the given Backend, which must be
// non-nil.
func New(backend Backend) *Adapter {
	if backend == nil {
		panic("eladapter.New: backend must not be nil")
	}
	return &Adapter{backend: backend}
}

// Compile-time assertion that *Adapter satisfies Caplin's ExecutionEngine.
// If upstream Caplin adds a new method this line breaks the n42el build
// instantly so the seam cannot ship partial.
var _ execution_client.ExecutionEngine = (*Adapter)(nil)

// --- Engine API surface ---------------------------------------------------

// NewPayload delegates to Backend.ExecutePayload, which owns the
// cltypes.Eth1Block → common/block.Block conversion and the real
// EngineStateAdapter call. Phase 7.1.1 wiring; the Backend may still
// return ErrNotImplemented while its conversion path is being filled in.
func (a *Adapter) NewPayload(
	ctx context.Context,
	blk *cltypes.Eth1Block,
	parentBeaconBlockRoot *depcommon.Hash,
	versionedHashes []depcommon.Hash,
	executionRequests []hexutil.Bytes,
) (execution_client.PayloadStatus, error) {
	return a.backend.ExecutePayload(ctx, blk, parentBeaconBlockRoot, versionedHashes, executionRequests)
}

// ForkChoiceUpdate delegates to Backend.UpdateForkchoice. Phase 7.1.1
// wiring; Backend implementations decide how to materialise the call
// (direct EngineStateAdapter.ForkchoiceUpdated or via EngineAPIv4
// in-process).
func (a *Adapter) ForkChoiceUpdate(
	ctx context.Context,
	head, safe, finalized depcommon.Hash,
	attrs *engine_types.PayloadAttributes,
	version clparams.StateVersion,
) ([]byte, error) {
	return a.backend.UpdateForkchoice(ctx, head, safe, finalized, attrs, version)
}

// --- Insertion path -------------------------------------------------------

// SupportInsertion stays false until Phase 7.1.2 has been exercised
// against mainnet. Returning false forces Caplin's stage loop onto the
// NewPayload path (which is also implemented as of 7.1.1.b); flipping
// to true unlocks batch backfill, which is faster but skips per-block
// FCU updates between inserts.
func (a *Adapter) SupportInsertion() bool { return false }

// InsertBlocks delegates to Backend.InsertBlocks. Phase 7.1.2 wiring;
// Backend chooses the storage strategy (erigon-style pure storage or
// N42's NewPayload-loop variant). See docs/ethel/caplin-phase-7-plan.md
// for the architecture analysis.
func (a *Adapter) InsertBlocks(ctx context.Context, blks []*deptypes.Block, wait bool) error {
	return a.backend.InsertBlocks(ctx, blks, wait)
}

// InsertBlock delegates to Backend.InsertBlock.
func (a *Adapter) InsertBlock(ctx context.Context, blk *deptypes.Block) error {
	return a.backend.InsertBlock(ctx, blk)
}

// --- Read-only chain access ----------------------------------------------

// CurrentHeader delegates to Backend.ReadCurrentHeader, which reads the
// canonical head from rawdb. Phase 7.1.1 wiring.
func (a *Adapter) CurrentHeader(ctx context.Context) (*deptypes.Header, error) {
	return a.backend.ReadCurrentHeader(ctx)
}

func (a *Adapter) IsCanonicalHash(ctx context.Context, hash depcommon.Hash) (bool, error) {
	return a.backend.IsCanonicalHash(ctx, hash)
}

func (a *Adapter) Ready(ctx context.Context) (bool, error) {
	return a.backend.Ready(ctx)
}

func (a *Adapter) HasBlock(ctx context.Context, hash depcommon.Hash) (bool, error) {
	return a.backend.HasBlock(ctx, hash)
}

// --- Range / body queries -------------------------------------------------

// GetBodiesByRange delegates to Backend.GetBodiesByRange (Phase 7.1.3).
func (a *Adapter) GetBodiesByRange(ctx context.Context, start, count uint64) ([]*deptypes.RawBody, error) {
	return a.backend.GetBodiesByRange(ctx, start, count)
}

// GetBodiesByHashes delegates to Backend.GetBodiesByHashes (Phase 7.1.3).
func (a *Adapter) GetBodiesByHashes(ctx context.Context, hashes []depcommon.Hash) ([]*deptypes.RawBody, error) {
	return a.backend.GetBodiesByHashes(ctx, hashes)
}

// --- Snapshot info --------------------------------------------------------

// FrozenBlocks returns 0; cmd/ethexec maintains no CL snapshots, so Caplin
// treats every block as needing live sync from genesis or checkpoint.
func (a *Adapter) FrozenBlocks(_ context.Context) uint64 { return 0 }

func (a *Adapter) HasGapInSnapshots(_ context.Context) bool { return false }

// --- Block production -----------------------------------------------------

// GetAssembledBlock delegates to Backend.GetAssembledBlock (Phase 7.1.4).
// Backends that don't run a builder (follower-mode N42) return
// errBlockProductionNotWired; Caplin treats that as "no payload" and
// falls through to a non-proposer path.
func (a *Adapter) GetAssembledBlock(
	ctx context.Context,
	idBytes []byte,
	version clparams.StateVersion,
) (*cltypes.Eth1Block, *engine_types.BlobsBundle, *typesproto.RequestsBundle, *big.Int, error) {
	return a.backend.GetAssembledBlock(ctx, idBytes, version)
}

// --- Blob retrieval -------------------------------------------------------

// GetBlobs delegates to Backend.GetBlobs (Phase 7.1.5). N42 eth-el does
// not maintain a blob store; follower-mode backends return empty
// slices.
func (a *Adapter) GetBlobs(
	ctx context.Context,
	versionedHashes []depcommon.Hash,
	version clparams.StateVersion,
) (blobs [][]byte, proofs [][][]byte, err error) {
	return a.backend.GetBlobs(ctx, versionedHashes, version)
}
