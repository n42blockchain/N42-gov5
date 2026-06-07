// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// NoopBeaconSnapshotReader is a consume-only BeaconSnapshotReader for the B+
// block-only fork choice driver (#34). The driver CONSUMES beacon blocks (from
// gossip + req/resp into the fork-choice fork graph) and does not need to SERVE
// historical blocks to peers from snapshots — so every read returns "absent"
// and the sentinel's block req/resp handlers simply find nothing to serve. The
// fork graph + index DB hold the blocks the driver actually needs.

//go:build n42el

package freezeblocks

import (
	"context"

	"github.com/n42blockchain/N42/internal/cl/cltypes"
	common "github.com/n42blockchain/N42/internal/cl/depshim/common"
	deptypes "github.com/n42blockchain/N42/internal/cl/depshim/types"
	"github.com/n42blockchain/N42/lib/kv"
)

type NoopBeaconSnapshotReader struct{}

var _ BeaconSnapshotReader = NoopBeaconSnapshotReader{}

func (NoopBeaconSnapshotReader) ReadBlockBySlot(_ context.Context, _ kv.Tx, _ uint64) (*cltypes.SignedBeaconBlock, error) {
	return nil, nil
}
func (NoopBeaconSnapshotReader) ReadBlockByRoot(_ context.Context, _ kv.Tx, _ common.Hash) (*cltypes.SignedBeaconBlock, error) {
	return nil, nil
}
func (NoopBeaconSnapshotReader) ReadHeaderByRoot(_ context.Context, _ kv.Tx, _ common.Hash) (*cltypes.SignedBeaconBlockHeader, error) {
	return nil, nil
}
func (NoopBeaconSnapshotReader) ReadBeaconBlockBodyBySlot(_ context.Context, _ kv.Tx, _ uint64) (*cltypes.SignedBeaconBlock, error) {
	return nil, nil
}
func (NoopBeaconSnapshotReader) FrozenSlots() uint64 { return 0 }
func (NoopBeaconSnapshotReader) CacheBlockBody(_ uint64, _ [][]byte, _ []*deptypes.Withdrawal) {
}
