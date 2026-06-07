//go:build n42el

// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// follower.go — the lightweight EL-follower consensus driver (#31 Phase 7,
// option B; #32 EL-tier wiring). Instead of the full beacon-gossip ClStages loop
// + sentinel/p2p stack, it bootstraps from an HTTP checkpoint-sync endpoint and
// drives the EL's Engine API forkChoiceUpdated:
//
//   - finalized/safe ← the finalized beacon checkpoint's execution block
//     (refreshed slowly over HTTP — finality advances ~1 epoch).
//   - head ← the EL's OWN current tip once it has synced at/past finalized
//     (the EL receives newer payloads via its eth/68 devp2p, so this tracks the
//     live tip at ~12 s without any beacon P2P); during catch-up, before the EL
//     reaches finalized, head = finalized so the EL syncs toward it.
//
// This is the model the minimal/full EL tiers use for their ~150 MB
// checkpoint-sync embed. Per tier it is enabled by launching eth-el with
// --caplin.checkpoint.url <beacon endpoint>.

package cl

import (
	"context"
	"time"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	common "github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/engineapi/engine_types"
	deptypes "github.com/n42blockchain/N42/internal/cl/depshim/types"
	"github.com/n42blockchain/N42/internal/cl/phase1/core/checkpoint_sync"
	"github.com/n42blockchain/N42/log"
)

// headDriveInterval is how often the follower re-evaluates the EL head and
// drives forkChoiceUpdated. One slot (~12 s) keeps the EL head live once synced.
const headDriveInterval = 12 * time.Second

// finalityRefreshInterval is how often the follower re-reads the finalized
// checkpoint. Finality advances ~1 epoch (6.4 min); we poll less aggressively
// because each GetLatestBeaconState downloads a full (~150 MB) finalized state.
//
// TODO(caplin-light): replace the full-state fetch with a light
// /eth/v1/beacon/headers/finalized poll + per-block execution-hash resolution to
// cut bandwidth (the full-state path is used because it decodes the execution
// header correctly across every fork, including Gloas, via the SSZ state
// decoder — the beacon-block JSON path is fork-version fragile).
const finalityRefreshInterval = 15 * time.Minute

// engineFollower is the subset of the EL execution engine the follower drives.
// *eladapter.Adapter satisfies it; defining it here lets the drive logic be
// unit-tested with a fake engine.
type engineFollower interface {
	CurrentHeader(ctx context.Context) (*deptypes.Header, error)
	ForkChoiceUpdate(ctx context.Context, finalized, safe, head common.Hash, attributes *engine_types.PayloadAttributes, version clparams.StateVersion) ([]byte, error)
}

// finalizedInfo is the execution view of the latest finalized beacon checkpoint.
type finalizedInfo struct {
	hash    common.Hash
	number  uint64
	version clparams.StateVersion
}

// fcuPointers is the last (finalized, head) pair driven, used to skip redundant
// forkChoiceUpdated calls.
type fcuPointers struct {
	finalized common.Hash
	head      common.Hash
}

// driveForkChoice computes the head to drive and, if (finalized, head) changed
// since `last`, issues forkChoiceUpdated. Head is the EL's own tip when it has
// reached finalized (live following), else finalized (catch-up target). Returns
// the new pointers and whether an update was issued. Pure w.r.t. its inputs so
// it is unit-tested with a fake engine.
func driveForkChoice(ctx context.Context, eng engineFollower, fin finalizedInfo, last fcuPointers) (fcuPointers, bool, error) {
	if fin.hash == (common.Hash{}) {
		return last, false, nil // no finalized checkpoint yet — wait for checkpoint-sync
	}
	head := fin.hash
	// If the EL has synced at/past the finalized block, follow its live tip
	// (delivered via the EL's own eth/68 devp2p) instead of pinning to finalized.
	if hdr, err := eng.CurrentHeader(ctx); err == nil && hdr != nil {
		if hdr.Number.Uint64() >= fin.number {
			if hh := hdr.Hash(); hh != (common.Hash{}) {
				head = hh
			}
		}
	}
	next := fcuPointers{finalized: fin.hash, head: head}
	if next == last {
		return last, false, nil
	}
	if _, err := eng.ForkChoiceUpdate(ctx, fin.hash, fin.hash, head, nil, fin.version); err != nil {
		return last, false, err
	}
	return next, true, nil
}

// runFinalityFollower runs the option-B follower until the service context is
// cancelled. Started by Service.Start when a CheckpointSyncURL is set.
func (s *Service) runFinalityFollower(beaconCfg *clparams.BeaconChainConfig, net clparams.NetworkType) {
	syncer := checkpoint_sync.NewRemoteCheckpointSync(beaconCfg, net)

	var fin finalizedInfo
	var last fcuPointers

	refreshFinalized := func() {
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
		fin = finalizedInfo{hash: hdr.BlockHash, number: hdr.BlockNumber, version: st.Version()}
		log.Info("Caplin follower: finalized checkpoint", "beaconSlot", st.Slot(),
			"execNumber", fin.number, "execHash", fin.hash)
	}

	drive := func() {
		next, updated, err := driveForkChoice(s.ctx, s.engine, fin, last)
		if err != nil {
			log.Warn("Caplin follower: forkChoiceUpdated failed", "head", next.head, "err", err)
			return
		}
		if updated {
			last = next
			log.Info("Caplin follower: drove EL fork choice", "finalized", next.finalized, "head", next.head)
		}
	}

	refreshFinalized() // bootstrap finalized
	drive()            // and an initial drive

	headTicker := time.NewTicker(headDriveInterval)
	defer headTicker.Stop()
	finTicker := time.NewTicker(finalityRefreshInterval)
	defer finTicker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-finTicker.C:
			refreshFinalized()
		case <-headTicker.C:
			drive()
		}
	}
}
