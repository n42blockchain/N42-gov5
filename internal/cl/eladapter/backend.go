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
// n42-el runtime. cmd/ethexec/beacon_backend.go provides the
// implementation. Methods are invoked from Caplin's stage loop and must
// be safe for concurrent use.
type Backend interface {
	CurrentHeadNumber(ctx context.Context) (uint64, error)
	HasBlock(ctx context.Context, hash depcommon.Hash) (bool, error)
	IsCanonicalHash(ctx context.Context, hash depcommon.Hash) (bool, error)
	Ready(ctx context.Context) (bool, error)
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

func (a *Adapter) NewPayload(
	_ context.Context,
	_ *cltypes.Eth1Block,
	_ *depcommon.Hash,
	_ []depcommon.Hash,
	_ []hexutil.Bytes,
) (execution_client.PayloadStatus, error) {
	return execution_client.PayloadStatusNotValidated, ErrNotImplemented
}

func (a *Adapter) ForkChoiceUpdate(
	_ context.Context,
	_, _, _ depcommon.Hash,
	_ *engine_types.PayloadAttributes,
	_ clparams.StateVersion,
) ([]byte, error) {
	return nil, ErrNotImplemented
}

// --- Insertion path -------------------------------------------------------

// SupportInsertion returns false to force Caplin onto the NewPayload path.
// Flip when InsertBlock(s) is wired to internal/ethel/executor.
func (a *Adapter) SupportInsertion() bool { return false }

func (a *Adapter) InsertBlocks(_ context.Context, _ []*deptypes.Block, _ bool) error {
	return ErrNotImplemented
}

func (a *Adapter) InsertBlock(_ context.Context, _ *deptypes.Block) error {
	return ErrNotImplemented
}

// --- Read-only chain access ----------------------------------------------

func (a *Adapter) CurrentHeader(_ context.Context) (*deptypes.Header, error) {
	return nil, ErrNotImplemented
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

func (a *Adapter) GetBodiesByRange(_ context.Context, _, _ uint64) ([]*deptypes.RawBody, error) {
	return nil, ErrNotImplemented
}

func (a *Adapter) GetBodiesByHashes(_ context.Context, _ []depcommon.Hash) ([]*deptypes.RawBody, error) {
	return nil, ErrNotImplemented
}

// --- Snapshot info --------------------------------------------------------

// FrozenBlocks returns 0; cmd/ethexec maintains no CL snapshots, so Caplin
// treats every block as needing live sync from genesis or checkpoint.
func (a *Adapter) FrozenBlocks(_ context.Context) uint64 { return 0 }

func (a *Adapter) HasGapInSnapshots(_ context.Context) bool { return false }

// --- Block production -----------------------------------------------------

func (a *Adapter) GetAssembledBlock(
	_ context.Context,
	_ []byte,
	_ clparams.StateVersion,
) (*cltypes.Eth1Block, *engine_types.BlobsBundle, *typesproto.RequestsBundle, *big.Int, error) {
	return nil, nil, nil, nil, ErrNotImplemented
}

// --- Blob retrieval -------------------------------------------------------

func (a *Adapter) GetBlobs(
	_ context.Context,
	_ []depcommon.Hash,
	_ clparams.StateVersion,
) (blobs [][]byte, proofs [][][]byte, err error) {
	return nil, nil, ErrNotImplemented
}
