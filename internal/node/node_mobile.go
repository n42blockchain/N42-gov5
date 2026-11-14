// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Mobile verifier identity and start helpers.

package node

import (
	"context"
	"fmt"
	"time"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
	dtorrent "github.com/n42blockchain/N42/internal/distributed/storage/torrent"
	"github.com/n42blockchain/N42/internal/mobileverify"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// mobileSelfIdentity returns a stable per-node identity for CohortCoordinator's
// cross-node cert/index announcements. Prefers Etherbase (the same identity
// every other N42 subsystem uses), since it needs no new derivation and
// stays consistent if the operator inspects logs/metrics across subsystems.
// Falls back to a hash of this node's libp2p peer ID for a pure follower
// with no configured account/keystore — startMobileVerify explicitly runs
// on non-miner nodes too, so Etherbase() erroring here must not be fatal.
// The value is never used for anything security-critical (only as a
// dedup/reporter tag in the cohort-merge gossip protocol), so an unstable
// fallback across restarts is a non-issue: it only ever needs to be stable
// for the lifetime of one run.
func (n *Node) mobileSelfIdentity() types.Address {
	if eb, err := n.Etherbase(); err == nil {
		return eb
	}
	if n.p2p != nil {
		h := crypto.Keccak256([]byte(n.p2p.PeerID().String()))
		var addr types.Address
		copy(addr[:], h[:20])
		return addr
	}
	return types.Address{}
}

// startMobileVerify assembles the mobile attestation packet pipeline
// (docs/mobile-attestation-design.md §4-5a): a rolling-window cache of
// encoded StreamPackets, a gossip service distributing them across the
// IDC fleet, and — when this node mines — the worker sink that produces
// a packet for every block this node seals. Disabled by default;
// consensus paths are untouched either way.
func (n *Node) startMobileVerify() error {
	if !n.config.MobileVerifyCfg.Enabled {
		return nil
	}
	cache := mobileverify.NewPacketCache(n.config.MobileVerifyCfg.PacketWindow)
	topic := fmt.Sprintf(p2p.MobilePacketTopicFormat, utils.ToBytes4(n.p2pGenesisHash[:]))
	if n.p2p != nil {
		topic += n.p2p.Encoding().ProtocolSuffix()
	}
	svc := mobileverify.NewPacketService(cache, n.p2p, topic)
	svc.SetHeadNumber(func() uint64 {
		if head := n.blockChain.CurrentBlock(); head != nil && head.Number64() != nil {
			return head.Number64().Uint64()
		}
		return 0
	})

	// Swarm distribution (design §5b target form): seed cached packets on
	// the BitTorrent bridge when both this pipeline and the torrent client
	// are enabled. Reuses the same bridge/client as eth-el cold-segment
	// distribution. Seeding is best-effort; failure never blocks the cache
	// or gossip. Wired before Start so the receive loop seeds inbound too.
	if n.config.MobileVerifyCfg.TorrentEnabled && n.config.TorrentDistCfg.Enabled {
		client, err := dtorrent.NewClient(&n.config.TorrentDistCfg)
		if err != nil {
			log.Error("mobileverify: torrent client init failed", "err", err)
		} else {
			client.Start()
			n.mobileTorrentClient = client
			bridge := dtorrent.NewBridge(client)
			svc.SetSeeder(&torrentPacketSeeder{bridge: bridge, pieceSize: n.config.TorrentDistCfg.PieceSize},
				int(n.config.MobileVerifyCfg.PacketWindow))
			log.Info("mobileverify: swarm seeding enabled")
		}
	}

	if n.p2p != nil {
		if err := svc.Start(); err != nil {
			// On a mining node with a scheduled MobileAnchor fork, a failed
			// mobileverify start means the registry/root provider below is never
			// wired, so post-fork the leader would stamp a nil MobileRegistryRoot
			// that every follower rejects. That is the exact state the pre-start
			// guard exists to prevent — make it fatal here too, not a log line.
			if n.config.NodeCfg.Miner && n.config.ChainCfg != nil && n.config.ChainCfg.MobileAnchorTime != nil {
				return fmt.Errorf("mobileverify packet service failed to start on a MobileAnchor mining node: %w", err)
			}
			log.Error("mobileverify: packet service failed to start", "err", err)
			return nil
		}
	}
	n.mobilePacketService = svc

	if n.miner != nil {
		n.miner.SetMobilePacketSink(func(pkt *evmsdk.StreamPacket, blockNumber uint64) {
			if err := svc.PublishLocal(pkt, blockNumber); err != nil {
				log.Warn("mobileverify: packet publish failed", "number", blockNumber, "err", err)
			}
		})
	}

	// Receipt intake: registry + collection windows + certificate store
	// (design §3, §5c, §6). The header lookup pins receipts to blocks this
	// node actually has — no allocation for arbitrary hashes.
	n.mobileRegistry = mobileverify.NewRegistry()
	n.mobileCertStore = mobileverify.NewCertStore(n.config.MobileVerifyCfg.CertBlocks)

	// Phase 6c (n42 native chain): supply the committed accumulator root the
	// leader stamps into the header when the MobileAnchor fork is active.
	// Dormant while MobileAnchorTime is nil (eth-el, and any n42 chainspec that
	// has not activated it) — the stamp only happens under IsMobileAnchor.
	if n.miner != nil {
		reg := n.mobileRegistry
		n.miner.SetMobileAnchorRoot(func() *types.Hash {
			root := reg.Root()
			return &root
		})
	}
	lookup := func(hash types.Hash) (uint64, bool) {
		var number uint64
		var ok bool
		_ = n.db.View(context.Background(), func(tx kv.Tx) error {
			if num := rawdb.ReadHeaderNumber(tx, hash); num != nil {
				number, ok = *num, true
			}
			return nil
		})
		return number, ok
	}
	// Block-height-coordinated cross-node cohort merge (replaces the earlier
	// per-node wall-clock WindowManager): at up to hundreds of IDC nodes, a
	// phone submitting to "any one node" meant every block could produce as
	// many small, unrelated certs as nodes that happened to receive a
	// submission — routing fragmentation, not the receipts-root divergence
	// the alarm pipeline exists to catch. CohortCoordinator merges every
	// node's cohort into one certificate per genuine outcome, using block
	// height (not each node's own clock) as the coordination primitive.
	n.mobileCohort = mobileverify.NewCohortCoordinator(
		n.mobileRegistry, lookup, n.mobileCertStore,
		n.mobileSelfIdentity(), mobileverify.DefaultCohortConfig(),
	)

	// Divergence alarms (design §6): a genuine post-merge multi-root outcome
	// lands in a bounded ring exposed via the status/RPC surface.
	n.mobileAlarms = mobileverify.NewAlarmBuffer(256)
	n.mobileCohort.SetDivergenceSink(n.mobileAlarms.Record)

	// Height is the shared clock every node's CohortCoordinator advances on
	// — wired generically through BlockChain so this package stays decoupled
	// from mobileverify (see SetOnBlockCommitted's doc comment).
	if realBC, ok := n.blockChain.(*internal.BlockChain); ok {
		realBC.SetOnBlockCommitted(n.mobileCohort.OnBlockCommitted)
	}

	// Cross-node cohort-merge gossip: index announcements (pre-aggregation
	// conflict exclusion) and cert announcements (the disjoint per-node
	// certs actually being merged). Both topics are cheap, best-effort
	// traffic — same lightweight scoring as the packet/registration topics.
	digest := utils.ToBytes4(n.p2pGenesisHash[:])
	cohortIndexTopic := fmt.Sprintf(p2p.MobileCohortIndexTopicFormat, digest)
	cohortRevealTopic := fmt.Sprintf(p2p.MobileCohortRevealTopicFormat, digest)
	cohortCertTopic := fmt.Sprintf(p2p.MobileCohortCertTopicFormat, digest)
	if n.p2p != nil {
		cohortIndexTopic += n.p2p.Encoding().ProtocolSuffix()
		cohortRevealTopic += n.p2p.Encoding().ProtocolSuffix()
		cohortCertTopic += n.p2p.Encoding().ProtocolSuffix()
	}
	// Layer 2C: raise the reconciliation ban threshold to f+1 distinct
	// authenticated reporters, so a single malicious validator can no longer
	// manufacture a ban. Derived DYNAMICALLY at each reconcile from the live
	// validator set: startMobileVerify runs during node startup, before the
	// HotStuff validator set is populated, so a one-shot read here would be nil
	// (or an empty set) and leave the threshold stuck at the floor of 2 instead
	// of f+1 on a larger fleet. The closure re-reads the set each time and falls
	// back to the safe default (2) off HotStuff or before the set exists.
	n.mobileCohort.SetBanThresholdFn(func() int {
		if vs := resolveBridgeValidatorSet(n.engine); vs != nil {
			return int(vs.FaultTolerance()) + 1
		}
		return 2
	})
	// Layer 2B: log reporters caught committing to a device index without a
	// valid proof-of-possession reveal (replay/fabrication attempts). These
	// are attributable to an authenticated validator and never counted toward
	// a ban — surfacing them is the accountability hook.
	n.mobileCohort.SetMisbehaviorSink(func(r mobileverify.MisbehaviorReport) {
		log.Warn("mobileverify: cohort reporter failed proof-of-possession reveal",
			"block", r.BlockNumber, "reporter", r.Reporter)
	})
	cohortSign, cohortVerify, authErr := n.buildCohortAuth()
	if authErr != nil {
		// Fail closed on a cohort participant: a mining node that gossips
		// announcements without authentication lets any peer spoof reporters and
		// manufacture bans (Layer 1's exact threat). Refuse to start rather than
		// run fail-open. Non-mining/legacy nodes fall through to a Warn.
		if n.config.NodeCfg.Miner {
			return fmt.Errorf("mobileverify: cohort reporter authentication unavailable on a mining node: %w", authErr)
		}
		log.Warn("mobileverify: cohort reporter authentication disabled", "err", authErr)
	}
	cohortRelay := mobileverify.NewCohortRelayWithAuth(n.mobileCohort, n.p2p, n.mobileRegistry, cohortIndexTopic, cohortRevealTopic, cohortCertTopic, cohortSign, cohortVerify)
	if n.p2p != nil {
		if err := cohortRelay.Start(); err != nil {
			log.Error("mobileverify: cohort relay failed to start", "err", err)
		}
	}
	n.mobileCohortRelay = cohortRelay

	// Registration replication (design §3): gossip new registrations across
	// the fleet so every node holds the same pending batch by epoch commit.
	regTopic := fmt.Sprintf(p2p.MobileRegistrationTopicFormat, utils.ToBytes4(n.p2pGenesisHash[:]))
	if n.p2p != nil {
		regTopic += n.p2p.Encoding().ProtocolSuffix()
	}
	regSvc := mobileverify.NewRegistrationService(n.mobileRegistry, n.p2p, regTopic)
	if n.p2p != nil {
		if err := regSvc.Start(); err != nil {
			log.Error("mobileverify: registration replication failed to start", "err", err)
		}
	}
	n.mobileRegService = regSvc

	// Epoch committer (design §3): periodically folds pending into the
	// committed accumulator and advances the on-chain-anchorable root. Each
	// commit persists the root to the anchor log (design §3.3) — a durable,
	// cross-checkable record; the state-root-committed anchor is the deferred
	// consensus-path step.
	n.mobileCommitter = mobileverify.NewEpochCommitter(n.mobileRegistry,
		time.Duration(n.config.MobileVerifyCfg.CollectWindowSec)*time.Second)
	// Continue the durable epoch sequence across restarts. The registry member
	// snapshot still needs its own persistence path (reported by the audit), but
	// a restart must at least not overwrite anchor records starting again at 1.
	var mobileEpoch uint64
	if err := n.db.View(context.Background(), func(tx kv.Tx) error {
		recent, err := rawdb.RecentMobileAnchors(tx, 1)
		if err != nil {
			return err
		}
		if len(recent) == 1 {
			mobileEpoch = recent[0].Epoch
		}
		return nil
	}); err != nil {
		log.Warn("mobileverify: failed to restore anchor epoch", "err", err)
	}
	n.mobileCommitter.SetOnCommit(func(root types.Hash, _ int) {
		mobileEpoch++
		var head uint64
		if cur := n.blockChain.CurrentBlock(); cur != nil {
			head = cur.Number64().Uint64()
		}
		if err := n.db.Update(context.Background(), func(tx kv.RwTx) error {
			return rawdb.WriteMobileAnchor(tx, rawdb.MobileAnchorRecord{
				Epoch: mobileEpoch, Root: root, HeadBlock: head, TimeMs: mobileverify.NowMs(),
			})
		}); err != nil {
			log.Warn("mobileverify: anchor persist failed", "epoch", mobileEpoch, "err", err)
		}
	})
	n.mobileCommitter.Start()

	// Phone-facing HTTP surface — its own listener, never the RPC ports.
	if addr := n.config.MobileVerifyCfg.HTTPAddr; addr != "" {
		httpSrv := mobileverify.NewHTTPServer(addr, n.mobileRegistry, svc, n.mobileCohort, n.mobileCertStore)
		httpSrv.SetAlarms(n.mobileAlarms)
		httpSrv.SetHead(func() (uint64, types.Hash, bool) {
			head := n.blockChain.CurrentBlock()
			if head == nil || head.Number64() == nil {
				return 0, types.Hash{}, false
			}
			return head.Number64().Uint64(), head.Hash(), true
		})
		// Sybil gate (design §7 item 2): registration proof-of-work. A device
		// attestor (Play Integrity / DeviceCheck adapter) can be wired here
		// once an operator supplies one; the HTTP gate is already in place.
		httpSrv.SetRegisterPoWBits(n.config.MobileVerifyCfg.RegisterPoWBits)
		if err := httpSrv.Start(); err != nil {
			log.Error("mobileverify: http server failed to start", "err", err)
		} else {
			n.mobileHTTPServer = httpSrv
		}
	}

	log.Info("Mobile attestation pipeline enabled",
		"window", n.config.MobileVerifyCfg.PacketWindow, "topic", topic,
		"producer", n.miner != nil, "http", n.config.MobileVerifyCfg.HTTPAddr)
	return nil
}
