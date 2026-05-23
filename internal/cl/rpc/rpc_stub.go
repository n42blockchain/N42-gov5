// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Phase 7.4 placeholder for ../erigon/cl/rpc/rpc.go — the
// BeaconRpcP2P concrete implementation depends on
// cl/sentinel/communication + sentinel/communication/ssz_snappy +
// cl/das/utils, a 3,000+ line subtree that's its own multi-day
// cherry-pick. This file declares the type shell so interface.go
// compiles (line "var _ BeaconRpc = (*BeaconRpcP2P)(nil)" needs the
// concrete type to exist). Method bodies land alongside the sentinel
// wiring in Phase 7.5.

//go:build n42el

package rpc

import (
	"context"
	"errors"

	"github.com/n42blockchain/N42/internal/cl/cltypes"
	"github.com/n42blockchain/N42/internal/cl/cltypes/solid"
	common "github.com/n42blockchain/N42/internal/cl/depshim/common"
)

// errBeaconRpcNotWired surfaces from every BeaconRpcP2P method until
// Phase 7.5 lands the sentinel subtree. Distinct from generic errors
// so the stage loop can switch on errors.Is.
var errBeaconRpcNotWired = errors.New(
	"rpc: BeaconRpcP2P not wired (Phase 7.5 placeholder)")

// BeaconRpcP2P is the concrete BeaconRpc client. Phase 7.4 ships an
// empty struct so the interface satisfaction assertion in
// interface.go compiles. Real implementation lives in
// ../erigon/cl/rpc/rpc.go and depends on the sentinel subtree.
type BeaconRpcP2P struct{}

func (*BeaconRpcP2P) SendBeaconBlocksByRangeReq(_ context.Context, _, _ uint64) ([]*cltypes.SignedBeaconBlock, string, error) {
	return nil, "", errBeaconRpcNotWired
}
func (*BeaconRpcP2P) SendBeaconBlocksByRootReq(_ context.Context, _ [][32]byte) ([]*cltypes.SignedBeaconBlock, string, error) {
	return nil, "", errBeaconRpcNotWired
}
func (*BeaconRpcP2P) SendBlobsSidecarByIdentifierReq(_ context.Context, _ *solid.ListSSZ[*cltypes.BlobIdentifier]) ([]*cltypes.BlobSidecar, string, error) {
	return nil, "", errBeaconRpcNotWired
}
func (*BeaconRpcP2P) SendBlobsSidecarByRangerReq(_ context.Context, _, _ uint64) ([]*cltypes.BlobSidecar, string, error) {
	return nil, "", errBeaconRpcNotWired
}
func (*BeaconRpcP2P) SendColumnSidecarsByRootIdentifierReq(_ context.Context, _ *solid.ListSSZ[*cltypes.DataColumnsByRootIdentifier]) ([]*cltypes.DataColumnSidecar, string, error) {
	return nil, "", errBeaconRpcNotWired
}
func (*BeaconRpcP2P) SendColumnSidecarsByRangeReqV1(_ context.Context, _, _ uint64, _ []uint64) ([]*cltypes.DataColumnSidecar, string, error) {
	return nil, "", errBeaconRpcNotWired
}
func (*BeaconRpcP2P) SendExecutionPayloadEnvelopesByRangeReq(_ context.Context, _, _ uint64) ([]*cltypes.SignedExecutionPayloadEnvelope, string, error) {
	return nil, "", errBeaconRpcNotWired
}
func (*BeaconRpcP2P) SendExecutionPayloadEnvelopesByRootReq(_ context.Context, _ [][32]byte) ([]*cltypes.SignedExecutionPayloadEnvelope, string, error) {
	return nil, "", errBeaconRpcNotWired
}
func (*BeaconRpcP2P) Peers() (uint64, error) {
	return 0, nil
}
func (*BeaconRpcP2P) SetStatus(_ common.Hash, _ uint64, _ common.Hash, _ uint64) error {
	return nil
}
func (*BeaconRpcP2P) BanPeer(_ string) {}
