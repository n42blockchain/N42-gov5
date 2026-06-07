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

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	common "github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/ethconfig"
	log "github.com/n42blockchain/N42/internal/cl/depshim/log/v3"
	"github.com/n42blockchain/N42/internal/cl/depshim/snapshotsync"
	"github.com/n42blockchain/N42/lib/common/datadir"
	"github.com/n42blockchain/N42/lib/kv"
)

// CaplinSnapshots is the beacon-block + blob-sidecar snapshot reader. The shim
// holds no segments; availability is always zero so consumers use the DB.
type CaplinSnapshots struct {
	beaconCfg *clparams.BeaconChainConfig
}

// NewCaplinSnapshots constructs an empty (DB-fallback) beacon snapshot set.
func NewCaplinSnapshots(cfg ethconfig.BlocksFreezing, beaconCfg *clparams.BeaconChainConfig, dirs datadir.Dirs, logger log.Logger) *CaplinSnapshots {
	return &CaplinSnapshots{beaconCfg: beaconCfg}
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
