// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Phase 7.2.6 — minimal-stub BlobStorage + DataColumnStorage interfaces.
//
// Background: ../erigon/cl/persistence/blob_storage/{blob_db.go,
// data_column_db.go} are real implementations that depend on
// cl/sentinel/communication/ssz_snappy (the sentinel subtree is
// multi-week cherry-pick work, see docs/ethel/caplin-phase-7-plan.md).
//
// What this file provides: just the two interface signatures one for
// one with upstream so the Phase 7.2.3 forkchoice.go cherry-pick can
// satisfy its BlobStorage / DataColumnStorage field types, plus a
// pair of Noop implementations that return safe-default values for
// follower-mode N42. Real implementations land alongside the sentinel
// network stack in Phase 7.4-7.5.

//go:build n42el

package blob_storage

import (
	"context"
	"errors"
	"io"

	"github.com/n42blockchain/N42/internal/cl/cltypes"
	"github.com/n42blockchain/N42/internal/cl/cltypes/solid"
	depcommon "github.com/n42blockchain/N42/internal/cl/depshim/common"
)

// VerifyAgainstIdentifiersAndInsertIntoTheBlobStore mirrors erigon's
// blob_storage package function used by the blob history downloader. The real
// KZG-verify-and-insert implementation lands with the blob store in Phase 7.4-7.5
// (alongside the sentinel stack); until then it surfaces errBlobStoreNotWired so
// blob backfill fails loud rather than silently dropping sidecars. The live
// follower path does not depend on historical blob backfill.
func VerifyAgainstIdentifiersAndInsertIntoTheBlobStore(
	ctx context.Context,
	storage BlobStorage,
	identifiers *solid.ListSSZ[*cltypes.BlobIdentifier],
	sidecars []*cltypes.BlobSidecar,
	verifySignatureFn func(header *cltypes.SignedBeaconBlockHeader) error,
) (uint64, uint64, error) {
	return 0, 0, errBlobStoreNotWired
}

// errBlobStoreNotWired surfaces from every fallible Noop method so
// fork-choice callers see an explicit "Phase 7.5 work pending" rather
// than a silent success that would corrupt blob retention windows.
var errBlobStoreNotWired = errors.New(
	"blob_storage: real store not wired (Phase 7.5 placeholder)")

// BlobStorage mirrors ../erigon/cl/persistence/blob_storage.BlobStorage
// one for one so the forkchoice cherry-pick compiles unchanged.
type BlobStorage interface {
	WriteBlobSidecars(ctx context.Context, blockRoot depcommon.Hash, blobSidecars []*cltypes.BlobSidecar) error
	RemoveBlobSidecars(ctx context.Context, slot uint64, blockRoot depcommon.Hash) error
	ReadBlobSidecars(ctx context.Context, slot uint64, blockRoot depcommon.Hash) (out []*cltypes.BlobSidecar, found bool, err error)
	BlobSidecarExists(ctx context.Context, slot uint64, blockRoot depcommon.Hash, idx uint64) (bool, error)
	WriteStream(w io.Writer, slot uint64, blockRoot depcommon.Hash, idx uint64) error
	KzgCommitmentsCount(ctx context.Context, blockRoot depcommon.Hash) (uint32, error)
	Prune() error
}

// DataColumnStorage mirrors the PeerDAS column storage interface.
type DataColumnStorage interface {
	WriteColumnSidecars(ctx context.Context, blockRoot depcommon.Hash, columnIndex int64, columnData *cltypes.DataColumnSidecar) error
	RemoveColumnSidecars(ctx context.Context, slot uint64, blockRoot depcommon.Hash, columnIndices ...int64) error
	RemoveAllColumnSidecars(ctx context.Context, slot uint64, blockRoot depcommon.Hash) error
	ReadColumnSidecarByColumnIndex(ctx context.Context, slot uint64, blockRoot depcommon.Hash, columnIndex int64) (*cltypes.DataColumnSidecar, error)
	ColumnSidecarExists(ctx context.Context, slot uint64, blockRoot depcommon.Hash, columnIndex int64) (bool, error)
	WriteStream(w io.Writer, slot uint64, blockRoot depcommon.Hash, idx uint64) error
	GetSavedColumnIndex(ctx context.Context, slot uint64, blockRoot depcommon.Hash) ([]uint64, error)
	Prune(keepSlotDistance uint64) error
}

// NoopBlobStore satisfies BlobStorage with safe defaults: writes
// succeed silently (so caller progress markers advance), reads return
// (nil, false, nil), counts return 0. Pre-Cancun mainnet doesn't
// exercise BlobStorage; the noop covers that surface.
type NoopBlobStore struct{}

// Compile-time check.
var _ BlobStorage = (*NoopBlobStore)(nil)

func (NoopBlobStore) WriteBlobSidecars(_ context.Context, _ depcommon.Hash, _ []*cltypes.BlobSidecar) error {
	return nil
}
func (NoopBlobStore) RemoveBlobSidecars(_ context.Context, _ uint64, _ depcommon.Hash) error {
	return nil
}
func (NoopBlobStore) ReadBlobSidecars(_ context.Context, _ uint64, _ depcommon.Hash) ([]*cltypes.BlobSidecar, bool, error) {
	return nil, false, nil
}
func (NoopBlobStore) BlobSidecarExists(_ context.Context, _ uint64, _ depcommon.Hash, _ uint64) (bool, error) {
	return false, nil
}
func (NoopBlobStore) WriteStream(_ io.Writer, _ uint64, _ depcommon.Hash, _ uint64) error {
	return errBlobStoreNotWired
}
func (NoopBlobStore) KzgCommitmentsCount(_ context.Context, _ depcommon.Hash) (uint32, error) {
	return 0, nil
}
func (NoopBlobStore) Prune() error { return nil }

// NoopColumnStore satisfies DataColumnStorage with safe defaults.
type NoopColumnStore struct{}

var _ DataColumnStorage = (*NoopColumnStore)(nil)

func (NoopColumnStore) WriteColumnSidecars(_ context.Context, _ depcommon.Hash, _ int64, _ *cltypes.DataColumnSidecar) error {
	return nil
}
func (NoopColumnStore) RemoveColumnSidecars(_ context.Context, _ uint64, _ depcommon.Hash, _ ...int64) error {
	return nil
}
func (NoopColumnStore) RemoveAllColumnSidecars(_ context.Context, _ uint64, _ depcommon.Hash) error {
	return nil
}
func (NoopColumnStore) ReadColumnSidecarByColumnIndex(_ context.Context, _ uint64, _ depcommon.Hash, _ int64) (*cltypes.DataColumnSidecar, error) {
	return nil, errBlobStoreNotWired
}
func (NoopColumnStore) ColumnSidecarExists(_ context.Context, _ uint64, _ depcommon.Hash, _ int64) (bool, error) {
	return false, nil
}
func (NoopColumnStore) WriteStream(_ io.Writer, _ uint64, _ depcommon.Hash, _ uint64) error {
	return errBlobStoreNotWired
}
func (NoopColumnStore) GetSavedColumnIndex(_ context.Context, _ uint64, _ depcommon.Hash) ([]uint64, error) {
	return nil, nil
}
func (NoopColumnStore) Prune(_ uint64) error { return nil }
