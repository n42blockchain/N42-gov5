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

// Start phases, called in order by (*Node).Start.

package node

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/grpc"

	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/accounts/keystore"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/debug"
	"github.com/n42blockchain/N42/internal/deferred"
	dcoprocessor "github.com/n42blockchain/N42/internal/distributed/coprocessor"
	"github.com/n42blockchain/N42/internal/mobileverify"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/snapshot"
	"github.com/n42blockchain/N42/internal/sync/backfill"
	"github.com/n42blockchain/N42/internal/sync/torrentsync"
	"github.com/n42blockchain/N42/internal/tracers"
	"github.com/n42blockchain/N42/lib/gointerfaces/remote"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/lib/kv/remotedbserver"
	log3 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	statesnapshot "github.com/n42blockchain/N42/modules/state/snapshot"
)

// startPreflight rejects an unusable mining configuration and moves the node
// into the running state.
func (n *Node) startPreflight() error {
	// A scheduled MobileAnchor fork makes MobileRegistryRoot mandatory in
	// HotStuff headers. Fail before starting any service if this node can become
	// leader but has no registry/root provider; otherwise it would appear healthy
	// until its turn, then repeatedly propose headers every follower rejects.
	if n.config.NodeCfg.Miner && n.config.ChainCfg != nil &&
		n.config.ChainCfg.MobileAnchorTime != nil && !n.config.MobileVerifyCfg.Enabled {
		return errors.New("mobileverify must be enabled on a mining node when MobileAnchorTime is configured")
	}

	n.lock.Lock()
	switch n.state {
	case runningState:
		n.lock.Unlock()
		return ErrNodeRunning
	case closedState:
		n.lock.Unlock()
		return ErrNodeStopped
	}
	n.state = runningState
	n.lock.Unlock()
	return nil
}

// startBlockChain starts the blockchain and the ExEx manager.
func (n *Node) startBlockChain() error {
	logEnvSwitches()
	log.PrintStartupProgress(1, 6, "Blockchain")
	if err := n.blockChain.Start(); err != nil {
		log.Errorf("failed setup blockChain service, err: %v", err)
		return err
	}

	// Start ExEx manager after blockchain is running.
	if n.exexManager != nil {
		n.exexManager.Start(n.ctx)
	}
	return nil
}

// startMining unlocks the etherbase in dev mode, authorizes the engine and
// starts the miner on a mining node.
func (n *Node) startMining() error {
	if n.config.NodeCfg.Miner {
		eb, err := n.Etherbase()
		if err != nil {
			log.Error("Cannot start mining without etherbase", "err", err)
			return fmt.Errorf("etherbase missing: %v", err)
		}

		// In dev mode, auto-unlock the etherbase account for signing.
		if n.config.NodeCfg.Chain == "private" {
			password := ""
			if n.config.NodeCfg.PasswordFile != "" {
				if data, readErr := os.ReadFile(n.config.NodeCfg.PasswordFile); readErr == nil {
					password = strings.TrimSpace(string(data))
				}
			}
			for _, backend := range n.accman.Backends(keystore.KeyStoreType) {
				if ks, ok := backend.(*keystore.KeyStore); ok {
					if unlockErr := ks.Unlock(accounts.Account{Address: eb}, password); unlockErr != nil {
						log.Warn("Failed to auto-unlock etherbase in dev mode", "err", unlockErr)
					} else {
						log.Info("Auto-unlocked etherbase for dev mode", "address", eb)
					}
				}
			}
		}

		if err := n.authorizeMiningEngine(eb); err != nil {
			return err
		}

		n.miner.SetCoinbase(eb)
		if s := n.localFrameSigner(eb); s != nil {
			n.miner.SetLocalFrameSigner(s)
		}
		n.miner.Start()
	}
	return nil
}

// startConsensus binds the APoS engine to the chain and starts the HotStuff
// service when the engine needs one.
func (n *Node) startConsensus(consensusPlan consensusRuntimePlan) error {
	if consensusPlan.bindAPOSBlockChain {
		pos := n.engine.(*apos.APos)
		pos.SetBlockChain(n.blockChain)
	}

	if consensusPlan.startHotStuffService {
		if err := n.startHotStuffService(); err != nil {
			return err
		}
	}
	return nil
}

// startHotStuffService creates, configures and starts the HotStuff service and
// wires it to the sync layer and the blockchain.
func (n *Node) startHotStuffService() error {
	hs, ok := resolveHotStuffEngine(n.engine)
	if !ok {
		return errors.New("hotstuff engine unavailable")
	}
	// Format gossip topic with fork digest (genesis hash prefix).
	forkDigest := utils.ToBytes4(n.p2pGenesisHash[:])
	gossipTopic := fmt.Sprintf(p2p.HotStuffConsensusTopicFormat, forkDigest)
	rpcTopic := p2p.RPCHotStuffDirectTopicV1
	svc := hotstuff.NewService(hs, newHotstuffP2PAdapter(n.p2p), n.db, gossipTopic, rpcTopic)
	if hs.Config().InteropV4 {
		if n.config.ChainCfg == nil || n.config.ChainCfg.ChainID == nil || !n.config.ChainCfg.ChainID.IsUint64() {
			return errors.New("hotstuff H2-v4 requires a uint64 chain ID")
		}
		identity := hotstuff.H2V4ChainIdentity{
			ChainID: n.config.ChainCfg.ChainID.Uint64(), GenesisHash: n.p2pGenesisHash,
		}
		if err := hs.EnableH2V4(identity); err != nil {
			return fmt.Errorf("enable hotstuff H2-v4: %w", err)
		}
		svc.SetH2V4Identity(identity)
	}
	if err := svc.ConfigureVoteStore(filepath.Join(n.config.NodeCfg.DataDir, "hotstuff-votes"),
		n.p2pGenesisHash, os.Getenv("N42_SEPARATE_VOTE_DB") == "1"); err != nil {
		return fmt.Errorf("configure hotstuff vote store: %w", err)
	}
	svc.SetBlockProducer(n.miner)
	// Committed-block vote floor: the engine never votes for, or locally
	// commits, a block that conflicts with what this node has committed,
	// whatever its (possibly reset) round state says.
	hs.SetCommittedChain(hotstuff.NewDBCommittedChain(n.ctx, n.db))
	if hotstuff.SeparateVoteJournalEnabled() && n.config.NodeCfg.DataDir != "" {
		jdb, err := openVoteJournalDB(n.ctx, filepath.Join(n.config.NodeCfg.DataDir, "hotstuff-journal"))
		if err != nil {
			return fmt.Errorf("open hotstuff vote journal: %w", err)
		}
		n.hotstuffJournalDB = jdb
		svc.SetVoteJournalDB(jdb)
		log.Info("hotstuff: votes journalled in a separate store (N42_VOTE_JOURNAL_SEPARATE)", "path", filepath.Join(n.config.NodeCfg.DataDir, "hotstuff-journal"))
	}
	if err := svc.Start(); err != nil {
		// A consensus service that failed to start (topic-join / gossip
		// registration error) leaves the node unable to vote or propose,
		// yet it would otherwise report "All services started" and appear
		// healthy while silently withholding participation — losing >f such
		// nodes halts BFT liveness. Fail Start instead of wiring a dead svc.
		return fmt.Errorf("hotstuff service failed to start: %w", err)
	}
	n.hotstuffService = svc

	// Wire sync layer → HotStuff: notify engine when blocks are imported, and
	// let the engine fetch a proposed block by hash when it's missing locally
	// (fetch-on-miss).
	n.sync.SetBlockImportNotifier(svc)
	svc.SetBlockFetcher(n.sync)

	// Early-vote path: the blockchain notifies consensus the moment a
	// block's execution and validation succeed, so the vote round overlaps
	// the block's persistence (deduplicated against the post-import notify
	// above).
	if realBC, ok := n.blockChain.(*internal.BlockChain); ok {
		realBC.SetExecutedHook(svc.NotifyBlockExecuted)
		// P2a: blocks held but not executed yet are not refetched or
		// reported as unexecuted commits; the lag executor's imports
		// retry deferred commits and resume a waiting leader view.
		svc.SetAvailabilityProbe(realBC.HasAvailableBlock)
		realBC.SetLagImportedHook(svc.NotifyBlockImported)
		realBC.SetLagResultHook(svc.NotifyLagResultReady)
		realBC.SetLagDrainedHook(svc.FlushPendingCommits)
		if err := realBC.StartE1Server(n.ctx); err != nil {
			log.Error("E=1 result server not started", "err", err)
		}
		if realBC.E1Light() {
			log.Warn("E=1 light node: execution results come from the executor (bench rule)")
		}
		if block.LagMode() && internal.FrameBlocksEnabled() {
			// Deferred depth k > 1 with frame blocks: the leader builds
			// without executing and may propose height N once it has
			// executed N-k.
			svc.SetLagGate(realBC.LagReady)
			log.Warn("Deferred execution depth above 1 (bench rule; every node must run the same N42_DEFERRED_DEPTH, N42_DEFERRED_DEPTH_PREV and N42_DEFERRED_DEPTH_FROM)",
				"depth", block.DeferredDepth(), "prev", block.DeferredDepthPrev(), "from", block.DeferredDepthFrom(), "voteOnAvailability", block.VoteOnAvailability())
		}
	}
	return nil
}

// startRPCAPIs assembles n.rpcAPIs and starts the bundler service.
func (n *Node) startRPCAPIs(consensusPlan consensusRuntimePlan) error {
	log.PrintStartupProgress(2, 6, "JSON-RPC services")

	n.rpcAPIs = append(n.rpcAPIs, n.engine.APIs(n.blockChain)...)
	n.rpcAPIs = append(n.rpcAPIs, n.api.Apis()...)
	n.rpcAPIs = append(n.rpcAPIs, jsonrpc.API{
		Namespace: "admin",
		Service:   api.NewAdminAPI(n.api),
	})
	engineAPIs := api.EngineAPIs(n.api)
	// ETH EL mode: wire state adapter into Engine API for persistent execution.
	if n.profile.IsEthereumEL() {
		if adapter := n.engineStateAdapter; adapter != nil {
			for i := range engineAPIs {
				switch svc := engineAPIs[i].Service.(type) {
				case *api.EngineAPIV1:
					svc.SetStateAdapter(adapter)
				case *api.EngineAPIBlob:
					svc.SetStateAdapter(adapter)
				case *api.EngineAPIv4:
					svc.SetStateAdapter(adapter)
				}
			}
		}
	}
	n.rpcAPIs = append(n.rpcAPIs, engineAPIs...)
	n.rpcAPIs = append(n.rpcAPIs, tracers.APIs(n.api)...)
	n.rpcAPIs = append(n.rpcAPIs, debug.APIs()...)

	// Register MEV bundle submission API.
	n.rpcAPIs = append(n.rpcAPIs, jsonrpc.API{
		Namespace: "eth",
		Service:   api.NewMevAPI(n.miner),
	})

	rpcPlan := resolveRPCExposurePlan(n.config, n.profile, n.bundlerService != nil, consensusPlan.registerHotStuffAdminAPI)

	// Register bundler RPC and start service.
	if rpcPlan.registerBundlerAPI {
		n.rpcAPIs = append(n.rpcAPIs, jsonrpc.API{
			Namespace: "eth",
			Service:   api.NewBundlerAPI(n.bundlerService),
		})
		n.bundlerService.Start(n.ctx)
	}

	if rpcPlan.registerSnapshotAPI {
		n.rpcAPIs = append(n.rpcAPIs, jsonrpc.API{
			Namespace: "debug",
			Service:   api.NewSnapshotAPI(n.db),
		})
	}

	if rpcPlan.registerWitnessAPI {
		n.rpcAPIs = append(n.rpcAPIs, jsonrpc.API{
			Namespace: "eth",
			Service:   api.NewWitnessAPI(n.api),
		})
	}

	if rpcPlan.registerZKProofAPI {
		n.rpcAPIs = append(n.rpcAPIs, api.NewZKProofAPI(n.api).APIs()...)
	}

	if rpcPlan.registerOtterscanAPI {
		n.rpcAPIs = append(n.rpcAPIs, api.OtterscanApis(n.api)...)
	}

	// mobileverify_* namespace: read-only queries over the attestation
	// pipeline (its own namespace by design — never consensus input).
	if n.mobileRegistry != nil {
		mvAPI := mobileverify.NewAPI(n.mobileRegistry, n.mobileCertStore, n.mobileCohort, n.mobileAlarms)
		mvAPI.SetAnchorReader(func(count int) []mobileverify.AnchorView {
			var out []mobileverify.AnchorView
			_ = n.db.View(context.Background(), func(tx kv.Tx) error {
				recs, err := rawdb.RecentMobileAnchors(tx, count)
				if err != nil {
					return err
				}
				for _, r := range recs {
					out = append(out, mobileverify.AnchorView{
						Epoch: r.Epoch, Root: r.Root.Hex(), HeadBlock: r.HeadBlock, TimeMs: r.TimeMs,
					})
				}
				return nil
			})
			return out
		})
		n.rpcAPIs = append(n.rpcAPIs, jsonrpc.API{
			Namespace: "mobileverify",
			Service:   mvAPI,
		})
	}

	// coprocessor_* namespace: submit/claim tasks, register providers, query
	// status, file challenges. The engine (tiered verify, marketplace,
	// slashing) was constructed but had no external surface before this.
	if n.coprocessorService != nil {
		n.rpcAPIs = append(n.rpcAPIs, jsonrpc.API{
			Namespace:     "coprocessor",
			Service:       dcoprocessor.NewAPI(n.coprocessorService),
			Authenticated: true,
		})
	}

	if rpcPlan.registerHotStuffAdminAPI {
		hs, ok := resolveHotStuffEngine(n.engine)
		if !ok {
			return errors.New("hotstuff engine unavailable")
		}
		n.rpcAPIs = append(n.rpcAPIs, jsonrpc.API{
			Namespace:     "admin",
			Service:       api.NewHotStuffReconfigAPI(func() *hotstuff.HotStuff { return hs }),
			Authenticated: true,
		})
	}
	return nil
}

// startNetworkingAndSync registers sync handlers, starts P2P, devp2p, sync,
// metrics and the deposit contract.
func (n *Node) startNetworkingAndSync() error {
	// Register sync handlers BEFORE P2P starts connecting peers.
	// This prevents the race where static peers connect before
	// the Status RPC handler is registered.
	n.sync.RegisterHandlers()

	log.PrintStartupProgress(3, 6, "P2P networking")
	n.p2p.Start()
	if err := n.startEthereumDevP2P(); err != nil {
		return err
	}

	log.PrintStartupProgress(4, 6, "Sync service")
	n.sync.Start()

	log.PrintStartupProgress(5, 6, "Metrics")
	n.SetupMetrics(n.config.MetricsCfg)

	log.PrintStartupProgress(6, 6, "Deposit contract")
	if n.depositContract != nil {
		n.depositContract.Start()
	}
	return nil
}

// startTorrentSync imports available EraE segments when OtterSync is enabled.
func (n *Node) startTorrentSync() {
	// Import available EraE segments before starting P2P sync.
	// This allows nodes to bootstrap from pre-computed chain data
	// distributed via BitTorrent, skipping re-execution of historical blocks.
	if n.config.TorrentSyncCfg.Enabled {
		eraDir := filepath.Join(n.config.NodeCfg.DataDir, "era")
		os.MkdirAll(eraDir, 0700)
		svc := torrentsync.NewService(&n.config.TorrentSyncCfg, n.db, eraDir)
		n.torrentsyncService = svc

		log.Info("OtterSync: importing available EraE segments", "dir", eraDir)
		if err := svc.Import(n.ctx); err != nil {
			log.Warn("OtterSync import failed, continuing with P2P sync", "err", err)
		}
	}
}

// startSyncPipeline launches checkpoint, snap and initial sync and the
// history backfill in the background.
func (n *Node) startSyncPipeline() {
	// Start sync pipeline: checkpoint sync (optional) -> snap sync -> initial sync.
	// Checkpoint sync validates a trusted block and sets it as the snap sync pivot.
	// Snap sync downloads state at the pivot. Initial sync replays remaining blocks.
	utils.SafeGo("node/sync-startup", func() {
		if n.checkpointSync != nil {
			pivot, err := n.checkpointSync.Start(n.ctx)
			if err != nil {
				log.Error("Checkpoint sync failed", "err", err)
			} else if pivot > 0 {
				log.Info("Checkpoint sync completed, snap sync will use checkpoint pivot", "pivot", pivot)
			}
		}
		n.snapSync.Start()
		n.is.Start()

		// Start background history backfill after snap sync pivot is established.
		backfillCfg := backfill.DefaultConfig()
		if backfillCfg.Enabled {
			currentBlock := n.blockChain.CurrentBlock().Number64().Uint64()
			n.backfillService = backfill.New(n.db, n.p2p, n.blockChain, backfillCfg)
			n.backfillService.Start(currentBlock)
		}
	})
}

// startCacheWarmers starts the snapshot acceleration and overlay cache warmers.
func (n *Node) startCacheWarmers() {
	// Start snapshot acceleration cache warmer if enabled.
	if n.config.SnapshotAccelCfg.Enable && n.config.SnapshotAccelCfg.WarmupOnStart {
		if cache := layered.ExtractCache(n.db); cache != nil {
			warmer := statesnapshot.NewWarmer(n.db, cache, n.config.SnapshotAccelCfg.WarmupAccounts)
			utils.SafeGo("snapshot-accel/warmup", func() {
				if err := warmer.Warm(n.ctx); err != nil {
					log.Warn("Snapshot acceleration warmup failed", "err", err)
				}
			})
		}
	}

	// Warm up overlay state cache from recent Account table entries.
	// Eliminates cold-cache penalty after node restart.
	if cache := layered.ExtractCache(n.db); cache != nil {
		utils.SafeGo("overlay-cache/warmup", func() {
			if err := state.WarmupOverlayCache(n.ctx, n.db, cache, 64); err != nil {
				log.Warn("Overlay cache warmup failed", "err", err)
			}
		})
	}
}

// startStorageMaintenance starts the snapshot manager, the deferred history
// backfiller, the pruner and the history expirer.
func (n *Node) startStorageMaintenance() {
	// Start snapshot manager if enabled
	if n.config.SnapshotCfg.Enable {
		bp := &nodeBlockProvider{node: n}
		n.snapshotMgr = snapshot.NewManager(n.config.SnapshotCfg, n.db, bp)
		n.snapshotMgr.Start()
	}

	// History index off the commit path: skipped inline in the state writer and
	// rebuilt from the changesets behind the head. Measured inline it costs
	// 130 ms of its own phase plus 266 ms of mdbx_txn_commit paying for the
	// pages its scattered rows dirty. Whether moving it off the block's
	// critical section keeps that saving -- or the backfiller gives it back
	// competing for the same MDBX write lock -- is what this mode measures.
	if state.HistoryIndexDeferred() {
		log.Warn("history index DEFERRED — rebuilt off the commit path from changesets; " +
			"historical queries are refused until the backfill marker reaches the snapshot head")
		n.historyBackfiller = internal.NewHistoryBackfiller(n.db,
			func() uint64 {
				if bc := n.blockChain; bc != nil {
					if cur := bc.CurrentBlock(); cur != nil {
						return cur.Number64().Uint64()
					}
				}
				return 0
			}, 256, historyBackfillInterval())
		n.historyBackfiller.Start()
	}

	// Start pruner if enabled
	if n.config.PruneCfg.IsEnabled() {
		hp := &nodeHealthProvider{node: n}
		var snap SnapshotBoundary
		if n.snapshotMgr != nil {
			snap = n.snapshotMgr
		}
		n.pruner = NewPruner(n.db, n.config.PruneCfg, hp, snap)
		n.pruner.Start()
	}

	// Start history expirer if enabled (EIP-4444 style)
	if n.config.HistoryExpiryCfg.IsEnabled() {
		hp := &nodeHealthProvider{node: n}
		n.historyExpirer = NewHistoryExpirer(n.db, n.config.HistoryExpiryCfg, hp)
		n.historyExpirer.Start()
		// Wire earliest-block gate into the sync service so that P2P range
		// requests for expired blocks are rejected.
		expirerEarliest := n.historyExpirer.EarliestBlock
		n.sync.SetEarliestBlock(func() uint64 {
			e := expirerEarliest()
			if a := rawdb.AncientEarliestExecBlock(); a > e {
				e = a
			}
			return e
		})
	} else if n.eraStore != nil {
		// Era-only gate: pruned/degraded exec eras answer range requests
		// with a typed "before earliest available" error instead of a
		// garbage short read.
		n.sync.SetEarliestBlock(rawdb.AncientEarliestExecBlock)
	}
}

// startPeerDAS starts the PeerDAS service if enabled.
func (n *Node) startPeerDAS() {
	// Start PeerDAS service if enabled
	if n.peerdasService != nil {
		if err := n.peerdasService.Start(n.ctx); err != nil {
			log.Warn("PeerDAS service failed to start", "err", err)
		}
	}
}

// startGRPCKVServer starts the gRPC KV server for RPCDaemon if configured.
func (n *Node) startGRPCKVServer() error {
	// Start gRPC KV server for RPCDaemon if configured.
	if addr := n.config.NodeCfg.PrivateAPIAddr; addr != "" {
		lis, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("failed to listen on private API addr %s: %w", addr, err)
		}
		grpcSrv := grpc.NewServer()
		kvServer := remotedbserver.NewKvServer(n.ctx, n.db, nil, nil, nil, log3.New())
		remote.RegisterKVServer(grpcSrv, kvServer)
		go func() {
			log.Info("gRPC KV server started for RPCDaemon", "addr", addr)
			if err := grpcSrv.Serve(lis); err != nil {
				log.Error("gRPC KV server error", "err", err)
			}
		}()
		n.grpcServer = grpcSrv
	}
	return nil
}

// startDeferredPipeline starts the deferred execution pipeline if configured.
func (n *Node) startDeferredPipeline() {
	// Start deferred execution pipeline if configured.
	if n.config.DeferredExec.Enabled {
		deferredCfg := deferred.PipelineConfig{
			Execution: deferred.Config{
				QueueSize: n.config.DeferredExec.QueueSize,
				Workers:   n.config.DeferredExec.Workers,
			},
		}
		execFn := deferred.NewEVMExecuteFunc(n.db, n.blockChain)
		commitFn := deferred.NewCommitFunc()
		n.deferredPipeline = deferred.NewPipeline(deferredCfg, execFn, commitFn)
		n.deferredPipeline.Start(n.ctx)
		log.Info("Deferred execution pipeline enabled",
			"queueSize", n.config.DeferredExec.QueueSize,
			"workers", n.config.DeferredExec.Workers,
		)
	}
}

// startZKProver starts the ZK prover service if configured.
func (n *Node) startZKProver() {
	// Start ZK prover service if configured.
	if n.zkProverService != nil {
		n.zkProverService.Start()
	}
}
