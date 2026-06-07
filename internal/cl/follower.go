//go:build n42el

// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// follower.go — the lightweight EL-follower consensus driver (#31 Phase 7,
// option B). Instead of the full beacon-gossip ClStages loop + sentinel/p2p
// stack, it bootstraps from an HTTP checkpoint-sync endpoint, reads the finalized
// beacon state's execution block hash, and drives the EL's Engine API
// forkChoiceUpdated. The EL syncs the execution payloads themselves via its own
// eth/68 devp2p; Caplin only pins the finalized/safe/head execution pointer to
// the finalized checkpoint. No beacon P2P is required — this is the approach the
// minimal/full EL tiers use for their ~150 MB checkpoint-sync embed.

package cl

import (
	"time"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	common "github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/phase1/core/checkpoint_sync"
	"github.com/n42blockchain/N42/log"
)

// finalityRefreshInterval is how often the follower re-reads the finalized
// checkpoint. Finality advances ~1 epoch (6.4 min); we poll less aggressively
// because each GetLatestBeaconState downloads a full (~150 MB) finalized state.
//
// TODO(caplin-light): replace the full-state fetch with a light
// /eth/v1/beacon/headers/finalized poll + per-block execution-hash resolution to
// cut bandwidth (the full-state path is used for now because it decodes the
// execution header correctly across every fork, including Gloas, via the SSZ
// state decoder — the beacon-block JSON path is fork-version fragile).
const finalityRefreshInterval = 15 * time.Minute

// runFinalityFollower runs the option-B follower loop until the service context
// is cancelled. It is started by Service.Start when a CheckpointSyncURL is set.
func (s *Service) runFinalityFollower(beaconCfg *clparams.BeaconChainConfig, net clparams.NetworkType) {
	syncer := checkpoint_sync.NewRemoteCheckpointSync(beaconCfg, net)

	apply := func() {
		st, err := syncer.GetLatestBeaconState(s.ctx)
		if err != nil {
			log.Warn("Caplin follower: fetch finalized state failed", "err", err)
			return
		}
		hdr := st.LatestExecutionPayloadHeader()
		if hdr == nil || hdr.BlockHash == (common.Hash{}) {
			log.Warn("Caplin follower: finalized state carries no execution payload header")
			return
		}
		execHash := hdr.BlockHash
		if execHash == s.lastFinalized {
			return // finalized execution block unchanged since last poll
		}
		// finalized = safe = head = the finalized checkpoint's execution block.
		// The EL pulls the payloads up to it via its own devp2p; we only pin the
		// canonical/finalized pointer. attributes=nil (we do not build blocks).
		if _, err := s.engine.ForkChoiceUpdate(s.ctx, execHash, execHash, execHash, nil, st.Version()); err != nil {
			log.Warn("Caplin follower: forkChoiceUpdated failed", "execHash", execHash, "err", err)
			return
		}
		s.lastFinalized = execHash
		log.Info("Caplin follower: drove EL to finalized checkpoint",
			"beaconSlot", st.Slot(), "execHash", execHash)
	}

	apply() // bootstrap immediately on start

	t := time.NewTicker(finalityRefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			apply()
		}
	}
}
