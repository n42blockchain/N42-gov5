//go:build n42el

// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// Ported from erigon cl/phase1/core/checkpoint_sync/util.go (FetchFinalizedEnvelope
// only) for the Caplin merge (#31). N42 already has the RemoteCheckpointSync
// .FetchFinalizedEnvelope method + CaplinConfig; this is the package-level
// wrapper the stage loop (forward_sync) calls to fetch the anchor execution
// payload envelope over HTTP checkpoint sync (Gloas/EIP-7732).

package checkpoint_sync

import (
	"context"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	"github.com/n42blockchain/N42/lib/log/v3"
)

// FetchFinalizedEnvelope fetches the finalized execution-payload envelope via
// remote (HTTP) checkpoint sync. Returns nil when remote sync is disabled, on a
// devnet without a custom checkpoint URL, or on any (non-fatal) fetch error —
// the caller then falls back to P2P.
func FetchFinalizedEnvelope(ctx context.Context, beaconCfg *clparams.BeaconChainConfig, caplinConfig clparams.CaplinConfig) *cltypes.SignedExecutionPayloadEnvelope {
	hasCustomCheckpointURL := len(clparams.ConfigurableCheckpointsURLs) > 0
	remoteSync := !caplinConfig.DisabledCheckpointSync && (!caplinConfig.IsDevnet() || hasCustomCheckpointURL)
	if !remoteSync {
		return nil
	}

	syncer := NewRemoteCheckpointSync(beaconCfg, caplinConfig.NetworkId).(*RemoteCheckpointSync)
	envelope, err := syncer.FetchFinalizedEnvelope(ctx)
	if err != nil {
		log.Warn("[Checkpoint Sync] Could not fetch finalized envelope (non-fatal)", "err", err)
		return nil
	}
	return envelope
}
