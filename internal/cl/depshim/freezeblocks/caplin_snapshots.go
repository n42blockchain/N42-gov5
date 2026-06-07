//go:build n42el

// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// Package freezeblocks is a DB-fallback depshim for erigon's
// db/snapshotsync/freezeblocks (Caplin parts). Like the snapshotsync shim, it
// reports an EMPTY beacon-block/blob snapshot set so all Caplin consumers
// (antiquary, phase1/stages, network downloaders) route reads through MDBX + the
// network rather than snapshot files. ReadHeader/ReadBlobSidecars return "not in
// snapshots" (nil) — callers only reach them for slots below BlocksAvailable(),
// which is 0 here, so they always take the DB path. The real snapshot
// reader/builder (archive-tier beacon-archive) is a separate later port (#31).

package freezeblocks

import (
	"context"
	"errors"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	common "github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/ethconfig"
	log "github.com/n42blockchain/N42/internal/cl/depshim/log/v3"
	"github.com/n42blockchain/N42/internal/cl/depshim/snapshotsync"
	deptypes "github.com/n42blockchain/N42/internal/cl/depshim/types"
	"github.com/n42blockchain/N42/internal/cl/persistence/blob_storage"
	"github.com/n42blockchain/N42/lib/common/datadir"
	"github.com/n42blockchain/N42/lib/kv"
)

// errSnapshotBuildUnsupported is returned by snapshot-BUILDING entry points
// (Dump*). N42's Caplin runs in DB-fallback (follower) mode with snapshot
// generation off, so these are never reached; an explicit error (not a silent
// no-op) fails loud if a caller is ever misconfigured to build snapshots.
var errSnapshotBuildUnsupported = errors.New("freezeblocks: snapshot build not supported in N42 DB-fallback shim (run with snapshot generation disabled)")

// BlobCountBySlotFn mirrors erigon's freezeblocks.BlobCountBySlotFn.
type BlobCountBySlotFn func(slot uint64) (uint64, error)

// BeaconSnapshotReader mirrors erigon's freezeblocks.BeaconSnapshotReader — the
// read interface Caplin consumers (historical_states_reader, antiquary) store
// and call to fetch beacon blocks/headers. The concrete implementation (backed
// by CaplinSnapshots + the DB) is wired by the caller; defining the interface
// here lets the consumers compile under the DB-fallback model.
type BeaconSnapshotReader interface {
	// ReadBlockBySlot reads the block at the given slot; nil if absent.
	ReadBlockBySlot(ctx context.Context, tx kv.Tx, slot uint64) (*cltypes.SignedBeaconBlock, error)
	ReadBlockByRoot(ctx context.Context, tx kv.Tx, blockRoot common.Hash) (*cltypes.SignedBeaconBlock, error)
	ReadHeaderByRoot(ctx context.Context, tx kv.Tx, blockRoot common.Hash) (*cltypes.SignedBeaconBlockHeader, error)
	// ReadBeaconBlockBodyBySlot reads a block without full execution payload data.
	ReadBeaconBlockBodyBySlot(ctx context.Context, tx kv.Tx, slot uint64) (*cltypes.SignedBeaconBlock, error)

	FrozenSlots() uint64
	// CacheBlockBody caches a recently produced block's execution body.
	// NOTE: erigon types this as execution/types.Withdrawal; N42's canonical
	// equivalent is depshim/types.Withdrawal, so the real BeaconSnapshotReader
	// impl wired in a later #31 step must use depshim/types.Withdrawal to satisfy
	// this interface.
	CacheBlockBody(blockNumber uint64, transactions [][]byte, withdrawals []*deptypes.Withdrawal)
}

// CaplinSnapshots is the beacon-block + blob-sidecar snapshot reader. The shim
// holds no segments; availability is always zero so consumers use the DB.
type CaplinSnapshots struct {
	// Salt is read by antiquary when building snapshots (unused in DB-fallback,
	// where the Dump* paths return errSnapshotBuildUnsupported). Exported to match
	// erigon's field so ported code compiles.
	Salt uint32

	beaconCfg *clparams.BeaconChainConfig
}

// NewCaplinSnapshots constructs an empty (DB-fallback) beacon snapshot set.
func NewCaplinSnapshots(cfg ethconfig.BlocksFreezing, beaconCfg *clparams.BeaconChainConfig, dirs datadir.Dirs, logger log.Logger) *CaplinSnapshots {
	return &CaplinSnapshots{beaconCfg: beaconCfg}
}

// DumpBeaconBlocks is a snapshot-build entry point — unsupported in DB-fallback.
func DumpBeaconBlocks(ctx context.Context, db kv.RoDB, fromSlot, toSlot uint64, salt uint32, dirs datadir.Dirs, workers int, lvl log.Lvl, logger log.Logger) error {
	return errSnapshotBuildUnsupported
}

// DumpBlobsSidecar is a snapshot-build entry point — unsupported in DB-fallback.
func DumpBlobsSidecar(ctx context.Context, blobStorage blob_storage.BlobStorage, db kv.RoDB, fromSlot, toSlot uint64, salt uint32, dirs datadir.Dirs, compressWorkers int, blobCountFn BlobCountBySlotFn, lvl log.Lvl, logger log.Logger) error {
	return errSnapshotBuildUnsupported
}

func (s *CaplinSnapshots) IndicesMax() uint64                                 { return 0 }
func (s *CaplinSnapshots) SegmentsMax() uint64                                { return 0 }
func (s *CaplinSnapshots) BlocksAvailable() uint64                            { return 0 }
func (s *CaplinSnapshots) FrozenBlobs() uint64                                { return 0 }
func (s *CaplinSnapshots) LogStat(str string)                                 {}
func (s *CaplinSnapshots) LS()                                                {}
func (s *CaplinSnapshots) SegFileNames(from, to uint64) []string              { return nil }
func (s *CaplinSnapshots) Close()                                             {}
func (s *CaplinSnapshots) OpenList(fileNames []string, optimistic bool) error { return nil }
func (s *CaplinSnapshots) OpenFolder() error                                  { return nil }

// BuildMissingIndices is a no-op: no segments → no indices.
func (s *CaplinSnapshots) BuildMissingIndices(ctx context.Context, logger log.Logger) error {
	return nil
}

// ReadHeader reports "not in snapshots" (nil header). Unreachable for the live
// path since BlocksAvailable()==0; callers read the header from the DB instead.
func (s *CaplinSnapshots) ReadHeader(slot uint64, tx kv.Tx) (*cltypes.SignedBeaconBlockHeader, uint64, common.Hash, error) {
	return nil, 0, common.Hash{}, nil
}

// ReadBlobSidecars reports "not in snapshots" (nil). Callers fall back to the DB.
func (s *CaplinSnapshots) ReadBlobSidecars(slot uint64) ([]*cltypes.BlobSidecar, error) {
	return nil, nil
}

// View returns an empty view (no beacon-block / blob segments).
func (s *CaplinSnapshots) View() *CaplinView { return &CaplinView{} }

// CaplinView is a point-in-time view over the (empty) beacon snapshots.
type CaplinView struct{}

func (v *CaplinView) Close() {}

// BeaconBlocks / BlobSidecars report no segments.
func (v *CaplinView) BeaconBlocks() []*snapshotsync.VisibleSegment { return nil }
func (v *CaplinView) BlobSidecars() []*snapshotsync.VisibleSegment { return nil }

// BeaconBlocksSegment / BlobSidecarsSegment always report "not covered".
func (v *CaplinView) BeaconBlocksSegment(slot uint64) (*snapshotsync.VisibleSegment, bool) {
	return nil, false
}

func (v *CaplinView) BlobSidecarsSegment(slot uint64) (*snapshotsync.VisibleSegment, bool) {
	return nil, false
}
