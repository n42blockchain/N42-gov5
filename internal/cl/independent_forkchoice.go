//go:build n42el

// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// independent_forkchoice.go — the B+ block-gossip-first INDEPENDENT fork choice
// driver (#34). Where follower.go blindly pins the EL to the EL's own eth/68
// devp2p tip (unsafe under an adversarial EL peer set — no attestation-weight
// check), this driver runs caplin's real LMD-GHOST + Casper FFG fork choice over
// beacon blocks gathered from a DIVERSE beacon peer set, and drives the EL to the
// independently-chosen head:
//
//	checkpoint-sync anchor state  ──►  ForkChoiceStore (engine = the EL)
//	         │                              ▲
//	         ▼                              │ OnBlock(fullValidation) per block
//	   p2p + sentinel  ──► BeaconRpcP2P ──► block sync loop (req/resp, catchup+live)
//	                                        │
//	                              OnTick ── ┴── GetHead ─► drive EL forkChoiceUpdated
//
// OnBlock(fullValidation=true) runs the state transition + signature checks, so a
// block that is execution-valid but NOT attestation-canonical cannot move the
// head — that is the adversarial-environment guarantee follower.go lacks. Live
// gossip (sub-slot latency) is a later refinement; req/resp range sync already
// gives attestation-weighted head selection from many peers.

package cl

import (
	"context"
	"fmt"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spf13/afero"

	"github.com/n42blockchain/N42/internal/cl/gossip"
	"github.com/n42blockchain/N42/internal/cl/utils"
	"github.com/n42blockchain/N42/internal/cl/beacon/beacon_router_configuration"
	"github.com/n42blockchain/N42/internal/cl/beacon/beaconevents"
	"github.com/n42blockchain/N42/internal/cl/beacon/synced_data"
	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	"github.com/n42blockchain/N42/internal/cl/cltypes/solid"
	"github.com/n42blockchain/N42/internal/cl/das"
	peerdasstate "github.com/n42blockchain/N42/internal/cl/das/state"
	common "github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/freezeblocks"
	"github.com/n42blockchain/N42/internal/cl/depshim/nat"
	"github.com/n42blockchain/N42/internal/cl/persistence/blob_storage"
	state2 "github.com/n42blockchain/N42/internal/cl/phase1/core/state"
	"github.com/n42blockchain/N42/internal/cl/phase1/core/checkpoint_sync"
	"github.com/n42blockchain/N42/internal/cl/phase1/forkchoice"
	"github.com/n42blockchain/N42/internal/cl/phase1/forkchoice/fork_graph"
	"github.com/n42blockchain/N42/internal/cl/phase1/forkchoice/public_keys_registry"
	"github.com/n42blockchain/N42/internal/cl/pool"
	clp2p "github.com/n42blockchain/N42/internal/cl/p2p"
	"github.com/n42blockchain/N42/internal/cl/rpc"
	clsentinel "github.com/n42blockchain/N42/internal/cl/sentinel"
	sentinelservice "github.com/n42blockchain/N42/internal/cl/sentinel/service"
	"github.com/n42blockchain/N42/internal/cl/utils/eth_clock"
	"github.com/n42blockchain/N42/internal/cl/validator/validator_params"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const (
	// blockSyncInterval is how often the req/resp block sync loop polls peers for
	// new beacon blocks (catchup + live tip following).
	blockSyncInterval = 4 * time.Second
	// blockSyncChunk is the max beacon-blocks-by-range count per request.
	blockSyncChunk = 64
	// headDriveInterval2 is how often we recompute the fork-choice head and drive
	// the EL. One head re-eval per few seconds keeps the EL head live.
	headDriveInterval2 = 2 * time.Second
	// lmdSamplingThreshold enables probabilistic head sampling above this many
	// active validators (mainnet-scale), matching erigon's run.go.
	lmdSamplingThreshold = 20_000
)

// runIndependentForkChoice bootstraps and runs the B+ independent fork choice
// driver until the service context is cancelled. Started by Service.Start when a
// sentinel discovery port is configured (otherwise the lightweight follower runs).
func (s *Service) runIndependentForkChoice(networkConfig *clparams.NetworkConfig, beaconCfg *clparams.BeaconChainConfig, net clparams.NetworkType) {
	logger := log.Root()

	// 1. Weak-subjectivity anchor: a finalized beacon state from checkpoint sync.
	syncer := checkpoint_sync.NewRemoteCheckpointSync(beaconCfg, net)
	anchorState, err := syncer.GetLatestBeaconState(s.ctx)
	if err != nil {
		log.Error("Caplin independent fork choice: fetch anchor state failed", "err", err)
		return
	}

	ethClock := eth_clock.NewEthereumClock(anchorState.GenesisTime(), anchorState.GenesisValidatorsRoot(), beaconCfg)

	// 2. Fork-choice store deps (all in-memory / noop for the block-only path).
	emitters := beaconevents.NewEventEmitter()
	syncedData := synced_data.NewSyncedDataManager(beaconCfg, true)
	opPool := pool.NewOperationsPool(beaconCfg)
	pksRegistry := public_keys_registry.NewInMemoryPublicKeysRegistry()
	valParams := validator_params.NewValidatorParams()
	blobStore := blob_storage.NoopBlobStore{}
	colStore := blob_storage.NoopColumnStore{}
	forkGraph := fork_graph.NewForkGraphDisk(anchorState, syncedData, afero.NewMemMapFs(), beacon_router_configuration.RouterConfiguration{}, emitters)

	doLMD := len(anchorState.GetActiveValidatorsIndices(anchorState.Slot()/beaconCfg.SlotsPerEpoch)) >= lmdSamplingThreshold
	fc, err := forkchoice.NewForkChoiceStore(
		ethClock, anchorState, s.engine, opPool, forkGraph, emitters, syncedData,
		blobStore, pksRegistry, valParams, doLMD, s.db)
	if err != nil {
		log.Error("Caplin independent fork choice: NewForkChoiceStore failed", "err", err)
		return
	}
	// PeerDAS is not used by block-only fork choice; a noop satisfies the
	// metadata/status reads sentinel.Identity makes via GetPeerDas().
	peerDasState := peerdasstate.NoopPeerDasState{}
	fc.InitPeerDas(das.NewNoopPeerDas(peerDasState))

	log.Info("Caplin independent fork choice: anchor loaded",
		"beaconSlot", anchorState.Slot(), "anchorRoot", fc.AnchorRoot(), "lmdSampling", doLMD)

	// 3. Beacon P2P: libp2p host + discv5, then the sentinel (req/resp server +
	//    peer mgmt) consumed in-process via the direct SentinelClient.
	// External IP advertisement (Docker/NAT): extip:<ip> | none.
	caplinNAT, err := nat.Parse(s.cfg.NAT)
	if err != nil {
		log.Error("Caplin independent fork choice: invalid NAT config", "nat", s.cfg.NAT, "err", err)
		return
	}
	// Append operator ENR bootnodes to the preset WITHOUT mutating the shared
	// network preset (clone struct + slice).
	if len(s.cfg.BootnodesENR) > 0 {
		nc := *networkConfig
		nc.BootNodes = append(append([]string{}, networkConfig.BootNodes...), s.cfg.BootnodesENR...)
		networkConfig = &nc
		log.Info("Caplin independent fork choice: added operator bootnodes", "count", len(s.cfg.BootnodesENR))
	}
	ipAddr := s.cfg.DiscoveryAddr
	if ipAddr == "" {
		ipAddr = "0.0.0.0"
	}
	maxPeers := s.cfg.MaxPeerCount
	if maxPeers == 0 {
		maxPeers = 64
	}
	p2pCfg := &clp2p.P2PConfig{
		NetworkConfig: networkConfig,
		BeaconConfig:  beaconCfg,
		IpAddr:        ipAddr,
		Port:          s.cfg.SentinelDiscoveryPort, // discv5 UDP
		TCPPort:       uint(s.cfg.SentinelPort),    // libp2p TCP (split from discovery)
		NAT:           caplinNAT,
		DataDir:       s.cfg.DataDir,
		TmpDir:        s.cfg.DataDir,
		MaxPeerCount:  maxPeers,
	}
	p2pManager, err := clp2p.NewP2Pmanager(s.ctx, p2pCfg, logger, ethClock)
	if err != nil {
		log.Error("Caplin independent fork choice: NewP2Pmanager failed", "err", err)
		return
	}

	forkDigest, err := ethClock.CurrentForkDigest()
	if err != nil {
		log.Error("Caplin independent fork choice: CurrentForkDigest failed", "err", err)
		return
	}
	anchorRoot := fc.AnchorRoot()
	initialStatus := &cltypes.Status{
		ForkDigest:     forkDigest,
		FinalizedRoot:  anchorRoot,
		FinalizedEpoch: fc.FinalizedCheckpoint().Epoch,
		HeadSlot:       anchorState.Slot(),
		HeadRoot:       anchorRoot,
	}
	sentinelClient, _, err := sentinelservice.StartSentinelService(
		s.ctx,
		&clsentinel.SentinelConfig{
			P2PConfig:    *p2pCfg,
			EnableBlocks: true,
		},
		freezeblocks.NoopBeaconSnapshotReader{},
		blobStore,
		s.db,
		&sentinelservice.ServerConfig{InitialStatus: initialStatus},
		ethClock,
		fc,
		colStore,
		peerDasState,
		p2pManager,
		logger,
	)
	if err != nil {
		log.Error("Caplin independent fork choice: StartSentinelService failed", "err", err)
		return
	}

	beaconRpc := rpc.NewBeaconRpcP2P(s.ctx, sentinelClient, beaconCfg, ethClock, anchorState)
	if err := beaconRpc.SetStatus(anchorRoot, fc.FinalizedCheckpoint().Epoch, anchorRoot, anchorState.Slot()); err != nil {
		log.Warn("Caplin independent fork choice: SetStatus failed", "err", err)
	}

	log.Info("Caplin independent fork choice: sentinel started — running attestation-weighted head selection")

	// 4. Drive loops.
	go s.forkChoiceTickLoop(fc)
	go s.blockSyncLoop(fc, beaconRpc, ethClock)
	go s.gossipBlockLoop(fc, p2pManager, ethClock, beaconCfg)
	s.headDriveLoop(fc, beaconCfg) // blocks until ctx done
}

// forkChoiceTickLoop advances the fork-choice clock (slot boundaries, proposer
// boost reset, etc.).
func (s *Service) forkChoiceTickLoop(fc *forkchoice.ForkChoiceStore) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			fc.OnTick(uint64(time.Now().Unix()))
		}
	}
}

// blockSyncLoop pulls beacon blocks by range from the peer set (req/resp) and
// feeds them into fork choice with full validation. It handles both catchup
// (anchor → tip) and live following (new slots) by always requesting up to the
// current clock slot. Blocks arrive from many peers, not a single trusted source.
func (s *Service) blockSyncLoop(fc *forkchoice.ForkChoiceStore, beaconRpc *rpc.BeaconRpcP2P, ethClock eth_clock.EthereumClock) {
	nextSlot := fc.AnchorSlot() + 1
	ticker := time.NewTicker(blockSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			curSlot := ethClock.GetCurrentSlot()
			for nextSlot <= curSlot {
				count := uint64(blockSyncChunk)
				if remaining := curSlot - nextSlot + 1; remaining < count {
					count = remaining
				}
				blocks, _, err := beaconRpc.SendBeaconBlocksByRangeReq(s.ctx, nextSlot, count)
				if err != nil {
					log.Trace("Caplin independent fork choice: block range req failed", "from", nextSlot, "err", err)
					break // try again next tick (peers may not have them yet)
				}
				if len(blocks) == 0 {
					break
				}
				applied := false
				var highest uint64
				for _, blk := range blocks {
					if blk == nil {
						continue
					}
					if err := fc.OnBlock(s.ctx, blk, true, true, false); err != nil {
						log.Trace("Caplin independent fork choice: OnBlock rejected", "slot", blk.Block.Slot, "err", err)
						continue
					}
					if !applied || blk.Block.Slot > highest {
						highest = blk.Block.Slot
						applied = true
					}
				}
				if applied {
					// Re-request from the slot after the highest block we actually
					// applied. Unlike a blind nextSlot += count, this never
					// permanently skips a tip slot whose block peers hadn't served
					// yet; genuine empty slots below `highest` are naturally passed.
					nextSlot = highest + 1
				} else {
					// Peers returned blocks but none applied (e.g. missing parent /
					// all in the future). Advance past the chunk to avoid stalling;
					// the gossip loop backstops live blocks.
					nextSlot += count
				}
			}
		}
	}
}

// gossipBlockLoop subscribes to the beacon_block gossip topic on the libp2p
// pubsub and feeds received blocks into fork choice with full validation. This
// gives sub-slot head latency (a freshly proposed block reaches OnBlock within
// the slot it was gossiped, vs the ~4s req/resp poll). It is a latency
// optimization layered on top of blockSyncLoop, which remains the reliable
// backstop (and covers fork-digest transitions, where the gossip topic name
// changes — this loop subscribes to the current fork digest only).
func (s *Service) gossipBlockLoop(fc *forkchoice.ForkChoiceStore, p2pManager clp2p.P2PManager, ethClock eth_clock.EthereumClock, beaconCfg *clparams.BeaconChainConfig) {
	ps := p2pManager.Pubsub()
	if ps == nil {
		return
	}
	forkDigest, err := ethClock.CurrentForkDigest()
	if err != nil {
		log.Warn("Caplin independent fork choice: gossip CurrentForkDigest failed", "err", err)
		return
	}
	version, err := ethClock.StateVersionByForkDigest(forkDigest)
	if err != nil {
		log.Warn("Caplin independent fork choice: gossip StateVersionByForkDigest failed", "err", err)
		return
	}
	// /eth2/[fork_digest]/beacon_block/ssz_snappy
	topicName := fmt.Sprintf("/eth2/%x/%s/%s", forkDigest, gossip.TopicNameBeaconBlock, gossip.SSZSnappyCodec)

	// Register a topic validator BEFORE subscribing. Without one, libp2p pubsub
	// relays every received message to mesh peers before our handler runs —
	// amplifying spam / invalid blocks to the network even though OnBlock would
	// reject them locally. The validator does cheap gate-keeping (snappy + SSZ
	// decode, drop far-future slots) and stashes the decoded block in
	// ValidatorData so the handler need not decode twice. Full state-transition
	// and signature checks still run in OnBlock.
	validator := func(_ context.Context, _ peer.ID, msg *pubsub.Message) pubsub.ValidationResult {
		decoded, err := utils.DecompressSnappy(msg.Data, true)
		if err != nil {
			return pubsub.ValidationReject
		}
		blk := cltypes.NewSignedBeaconBlock(beaconCfg, version)
		if err := blk.DecodeSSZ(decoded, int(version)); err != nil {
			return pubsub.ValidationReject
		}
		// Allow one slot of clock disparity; anything further ahead is spam or a
		// clock attack — ignore (do not propagate, do not penalize: it may become
		// valid once our clock catches up).
		if blk.Block.Slot > ethClock.GetCurrentSlot()+1 {
			return pubsub.ValidationIgnore
		}
		msg.ValidatorData = blk
		return pubsub.ValidationAccept
	}
	if err := ps.RegisterTopicValidator(topicName, validator); err != nil {
		log.Warn("Caplin independent fork choice: gossip RegisterTopicValidator failed", "topic", topicName, "err", err)
		return
	}
	defer func() { _ = ps.UnregisterTopicValidator(topicName) }()

	topic, err := ps.Join(topicName)
	if err != nil {
		log.Warn("Caplin independent fork choice: gossip Join failed", "topic", topicName, "err", err)
		return
	}
	sub, err := topic.Subscribe()
	if err != nil {
		log.Warn("Caplin independent fork choice: gossip Subscribe failed", "topic", topicName, "err", err)
		return
	}
	defer sub.Cancel()
	log.Info("Caplin independent fork choice: subscribed to beacon_block gossip", "topic", topicName)

	for {
		msg, err := sub.Next(s.ctx)
		if err != nil {
			if s.ctx.Err() != nil {
				return
			}
			log.Trace("Caplin independent fork choice: gossip Next failed", "err", err)
			continue
		}
		// Only Accept-ed messages reach the subscription, with the decoded block
		// stashed by the validator above.
		blk, ok := msg.ValidatorData.(*cltypes.SignedBeaconBlock)
		if !ok {
			continue
		}
		if err := fc.OnBlock(s.ctx, blk, true, true, false); err != nil {
			log.Trace("Caplin independent fork choice: gossip OnBlock rejected", "slot", blk.Block.Slot, "err", err)
		}
	}
}

// headResolver is the slice of ForkChoiceStore the head-drive loop reads. It
// exists so the adversarial-environment guarantee — that the EL head is sourced
// ENTIRELY from the attestation-weighted fork choice and NEVER from the EL's own
// devp2p tip — is unit-testable with a fake (see independent_forkchoice_test.go).
type headResolver interface {
	GetHead(*state2.CachingBeaconState) (common.Hash, uint64, error)
	GetEth1Hash(common.Hash) common.Hash
	GetFinalizedExecutionHash(common.Hash) common.Hash
	FinalizedCheckpoint() solid.Checkpoint
}

// resolveHeadExec computes the execution (finalized, head) block hashes to drive
// the EL to. Both are sourced from the fork choice r: head = the execution hash
// of the LMD-GHOST + FFG head block; finalized = the finalized checkpoint's
// execution hash. The EL's own tip is never consulted — that is precisely what
// makes this adversarial-safe vs follower.go. ok=false means the head's
// execution payload is not known yet (skip this round, do not drive the EL).
func resolveHeadExec(r headResolver, beaconCfg *clparams.BeaconChainConfig) (finalizedExec, headExec common.Hash, version clparams.StateVersion, ok bool, err error) {
	headRoot, headSlot, err := r.GetHead(nil)
	if err != nil {
		return common.Hash{}, common.Hash{}, 0, false, err
	}
	headExec = r.GetEth1Hash(headRoot)
	if headExec == (common.Hash{}) {
		return common.Hash{}, common.Hash{}, 0, false, nil
	}
	// finalized = the finalized checkpoint's execution hash. When the finalized
	// payload is not yet known (cold start, before the finalized block is in the
	// fork graph) we leave it ZERO — the Engine API reads a zero finalized hash
	// as "no finalized block", which is safe. We must NEVER fall back to head
	// here: asserting the (still-reorg-able) head as finalized would invite the
	// EL to irreversibly commit/prune state on a block fork choice may abandon.
	finalizedExec = r.GetFinalizedExecutionHash(r.FinalizedCheckpoint().Root)
	version = beaconCfg.GetCurrentStateVersion(headSlot / beaconCfg.SlotsPerEpoch)
	return finalizedExec, headExec, version, true, nil
}

// headDriveLoop recomputes the fork-choice head and drives the EL's Engine API
// forkChoiceUpdated to the INDEPENDENTLY chosen head (replacing follower.go's
// blind EL-tip following). Blocks until the service context is cancelled.
func (s *Service) headDriveLoop(fc *forkchoice.ForkChoiceStore, beaconCfg *clparams.BeaconChainConfig) {
	ticker := time.NewTicker(headDriveInterval2)
	defer ticker.Stop()
	var last fcuPointers
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			finalizedExec, headExec, version, ok, err := resolveHeadExec(fc, beaconCfg)
			if err != nil {
				log.Trace("Caplin independent fork choice: GetHead failed", "err", err)
				continue
			}
			if !ok {
				continue // head's execution payload not yet known
			}
			next := fcuPointers{finalized: finalizedExec, head: headExec}
			if next == last {
				continue
			}
			if _, err := s.engine.ForkChoiceUpdate(s.ctx, finalizedExec, finalizedExec, headExec, nil, version); err != nil {
				log.Warn("Caplin independent fork choice: forkChoiceUpdated failed", "head", headExec, "err", err)
				continue
			}
			last = next
			log.Info("Caplin independent fork choice: drove EL to attestation-weighted head",
				"headExec", headExec, "finalizedExec", finalizedExec)
		}
	}
}
