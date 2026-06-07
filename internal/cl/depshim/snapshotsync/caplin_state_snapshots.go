//go:build n42el

// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// Package snapshotsync is a DB-fallback depshim for erigon's db/snapshotsync
// (Caplin parts). Erigon's real implementation pulls in a large foreign
// snapshot-distribution subsystem (snaptype, snapcfg, db/state SnapNameSchema,
// version) that parallels N42's own freezer/columnar storage. The Caplin
// consumers (state_accessors, antiquary, phase1/stages, network downloaders) all
// tolerate an EMPTY snapshot set: they guard on `BlocksAvailable()==0` /
// `SegmentsMax()==0` / a nil View, and fall back to reading from MDBX + the
// network. So this shim reports "no snapshots" everywhere, which routes every
// read through the DB path. The real historical snapshot reader/builder (for the
// archive-tier beacon-archive) is a later, separate port; it is NOT needed for
// the checkpoint-sync + live-follow critical path (#31).
//
// API surface mirrors erigon's exactly so ported consumers compile unchanged.

package snapshotsync

import (
	"context"
	"errors"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/depshim/ethconfig"
	log "github.com/n42blockchain/N42/internal/cl/depshim/log/v3"
	"github.com/n42blockchain/N42/lib/common/datadir"
)

// errSnapshotBuildUnsupported is returned by snapshot-BUILDING entry points.
// N42's Caplin runs in DB-fallback (follower) mode with snapshot generation off,
// so these are never reached; surfacing an explicit error (rather than a silent
// no-op) fails loud if a caller is ever misconfigured to build snapshots.
var errSnapshotBuildUnsupported = errors.New("snapshotsync: snapshot build not supported in N42 DB-fallback shim (run with snapshot generation disabled)")

// VisibleSegment is the opaque per-segment handle erigon hands back from a view.
// In DB-fallback mode a view never returns a non-nil segment, so Get is never
// reached; it is defined only so signatures match.
type VisibleSegment struct{}

// Get returns the value for a global id within the segment. Unreachable in
// DB-fallback mode (the view returns no segments).
func (s *VisibleSegment) Get(globalId uint64) ([]byte, error) { return nil, nil }

// VisibleSegments is the slice form erigon returns from VisibleSegments(tbl).
type VisibleSegments []*VisibleSegment

// KeyValueGetter mirrors erigon's per-table snapshot value reader.
type KeyValueGetter func(numId uint64) ([]byte, []byte, error)

// SnapshotTypes describes the per-table getters/compression the state snapshot
// would cover. Carried for constructor fidelity; ignored by the shim.
type SnapshotTypes struct {
	KeyValueGetters map[string]KeyValueGetter
	Compression     map[string]bool
}

// CaplinStateSnapshots is the state-snapshot reader. The shim holds no segments;
// every availability query reports zero so consumers use the DB.
type CaplinStateSnapshots struct {
	beaconCfg *clparams.BeaconChainConfig
}

// NewCaplinStateSnapshots constructs an empty (DB-fallback) state-snapshot set.
func NewCaplinStateSnapshots(cfg ethconfig.BlocksFreezing, beaconCfg *clparams.BeaconChainConfig, dirs datadir.Dirs, snapshotTypes SnapshotTypes, logger log.Logger) *CaplinStateSnapshots {
	return &CaplinStateSnapshots{beaconCfg: beaconCfg}
}

func (s *CaplinStateSnapshots) IndicesMax() uint64                                 { return 0 }
func (s *CaplinStateSnapshots) SegmentsMax() uint64                                { return 0 }
func (s *CaplinStateSnapshots) BlocksAvailable() uint64                            { return 0 }
func (s *CaplinStateSnapshots) LogStat(str string)                                 {}
func (s *CaplinStateSnapshots) LS()                                                {}
func (s *CaplinStateSnapshots) SegFileNames(from, to uint64) []string              { return nil }
func (s *CaplinStateSnapshots) Close()                                             {}
func (s *CaplinStateSnapshots) OpenList(fileNames []string, optimistic bool) error { return nil }
func (s *CaplinStateSnapshots) OpenFolder() error                                  { return nil }
func (s *CaplinStateSnapshots) Get(tbl string, slot uint64) ([]byte, error)        { return nil, nil }

// BuildMissingIndices is a no-op: with no segments there are no indices to build.
func (s *CaplinStateSnapshots) BuildMissingIndices(ctx context.Context, logger log.Logger) error {
	return nil
}

// DumpCaplinState is a snapshot-build entry point — unsupported in DB-fallback.
func (s *CaplinStateSnapshots) DumpCaplinState(ctx context.Context, fromSlot, toSlot, blocksPerFile uint64, salt uint32, dirs datadir.Dirs, workers int, lvl log.Lvl, logger log.Logger) error {
	return errSnapshotBuildUnsupported
}

// View returns an empty view (no visible segments → DB fallback in callers).
func (s *CaplinStateSnapshots) View() *CaplinStateView { return &CaplinStateView{} }

// CaplinStateView is a point-in-time view over the (empty) state snapshots.
type CaplinStateView struct{}

func (v *CaplinStateView) Close() {}

// VisibleSegments reports no segments for any table.
func (v *CaplinStateView) VisibleSegments(tbl string) []*VisibleSegment { return nil }

// VisibleSegment always reports "not covered" so the caller reads from the DB.
func (v *CaplinStateView) VisibleSegment(slot uint64, tbl string) (*VisibleSegment, bool) {
	return nil, false
}
