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
// Node: top-level N42 process orchestrator. Creates and wires the
// database, consensus engine (apoa / apos / hotstuff), miner, txpool,
// P2P, JSON-RPC, MCP, ZK prover, deferred executor, gRPC KV server and
// distributed messaging / compute services, then manages the full
// lifecycle via Start and Stop. Also owns graceful shutdown ordering
// across subsystems.

package node

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gofrs/flock"
	"github.com/urfave/cli/v2"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/accounts/keystore"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/envswitch"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/contracts/deposit"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/ai/attestation"
	"github.com/n42blockchain/N42/internal/ai/coord"
	"github.com/n42blockchain/N42/internal/ai/governance"
	"github.com/n42blockchain/N42/internal/ai/training"
	"github.com/n42blockchain/N42/internal/ai/wallet"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/bridge"
	"github.com/n42blockchain/N42/internal/bundler"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	ddngateway "github.com/n42blockchain/N42/internal/ddn/gateway"
	ddnreceipt "github.com/n42blockchain/N42/internal/ddn/receipt"
	"github.com/n42blockchain/N42/internal/debug"
	"github.com/n42blockchain/N42/internal/deferred"
	ethdevp2p "github.com/n42blockchain/N42/internal/devp2p"
	"github.com/n42blockchain/N42/internal/distributed/compute/inference"
	dcoprocessor "github.com/n42blockchain/N42/internal/distributed/coprocessor"
	dmessaging "github.com/n42blockchain/N42/internal/distributed/messaging"
	dnotify "github.com/n42blockchain/N42/internal/distributed/notify"
	dstorage "github.com/n42blockchain/N42/internal/distributed/storage"
	dtorrent "github.com/n42blockchain/N42/internal/distributed/storage/torrent"
	"github.com/n42blockchain/N42/internal/exex"
	"github.com/n42blockchain/N42/internal/frame/attest"
	"github.com/n42blockchain/N42/internal/ingest"
	"github.com/n42blockchain/N42/internal/mcp"
	"github.com/n42blockchain/N42/internal/miner"
	"github.com/n42blockchain/N42/internal/mobileverify"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/peerdas"
	"github.com/n42blockchain/N42/internal/snapshot"
	"github.com/n42blockchain/N42/internal/stateless"
	n42sync "github.com/n42blockchain/N42/internal/sync"
	"github.com/n42blockchain/N42/internal/sync/backfill"
	"github.com/n42blockchain/N42/internal/sync/checkpoint"
	initialsync "github.com/n42blockchain/N42/internal/sync/initialsync"
	"github.com/n42blockchain/N42/internal/sync/snapsync"
	"github.com/n42blockchain/N42/internal/sync/torrentsync"
	"github.com/n42blockchain/N42/internal/tracers"
	"github.com/n42blockchain/N42/internal/txgen"
	"github.com/n42blockchain/N42/internal/txindexer"
	"github.com/n42blockchain/N42/internal/zkprover"
	"github.com/n42blockchain/N42/internal/zkverifier"
	"github.com/n42blockchain/N42/lib/gointerfaces/remote"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/lib/kv/remotedbserver"
	log3 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rawdb/ancientera"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	statesnapshot "github.com/n42blockchain/N42/modules/state/snapshot"
	"github.com/n42blockchain/N42/params"
)

const datadirJWTKey = "jwtsecret" // Path within the datadir to the node's jwt secret

type Node struct {
	cliCtx       *cli.Context
	ctx          context.Context
	cancel       context.CancelFunc
	config       *conf.Config
	profile      params.ProfileDescriptor
	runtimePlan  auxiliaryRuntimePlan
	genesisBlock block.IBlock
	etherbase    types.Address

	lock          sync.RWMutex  // Protects the variadic fields (e.g. gas price and etherbase)
	startStopLock sync.Mutex    // Start/Stop are protected by an additional lock
	state         int           // Tracks state of node lifecycle
	shutDown      chan struct{} // Channel to wait for termination notifications
	dirLock       *flock.Flock  // prevents concurrent use of instance directory

	miner           *miner.Miner
	blockChain      common.IBlockChain
	engine          consensus.Engine
	db              kv.RwDB
	txspool         common.ITxsPool
	depositContract *deposit.Deposit
	p2p             p2p.P2P
	devp2pServer    *ethdevp2p.Server
	sync            *n42sync.Service
	is              *initialsync.Service
	checkpointSync  *checkpoint.Service
	snapSync        *snapsync.Service
	accman          *accounts.Manager

	api                *api.API
	rpcAPIs            []jsonrpc.API
	engineStateAdapter *api.EngineStateAdapter // ETH EL mode only

	http              *httpServer
	ipc               *ipcServer
	ws                *httpServer
	httpAuth          *httpServer
	wsAuth            *httpServer
	inprocHandler     *jsonrpc.Server
	rateLimiter       *jsonrpc.RateLimiter
	pruner            *Pruner
	historyBackfiller *internal.HistoryBackfiller
	historyExpirer    *HistoryExpirer
	snapshotMgr       *snapshot.Manager

	p2pGenesisHash     types.Hash              // genesis hash used for P2P fork digest
	exexManager        *exex.Manager           // Execution Extensions manager
	hotstuffService    *hotstuff.Service       // HotStuff BFT consensus service (nil if not using HotStuff)
	hotstuffJournalDB  kv.RwDB                 // separate vote journal store (nil unless enabled)
	frameDB            kv.RwDB                 // frame store (nil unless N42_FRAME_DB)
	bundlerService     *bundler.BundlerService // ERC-4337 bundler service (nil if disabled)
	peerdasService     *peerdas.Service        // PeerDAS (EIP-7594) data availability service (nil if disabled)
	ddnSigner          *ddnreceipt.Signer
	ddnGateway         *ddngateway.Gateway
	mcpServer          *mcp.Server                 // MCP (Model Context Protocol) server for AI agents (nil if disabled)
	deferredPipeline   *deferred.Pipeline          // Deferred execution pipeline (nil if disabled)
	grpcServer         *grpc.Server                // gRPC KV server for RPCDaemon (nil if disabled)
	coprocessorService *dcoprocessor.Service       // ZK coprocessor (nil if disabled)
	messagingService   *dmessaging.Service         // Decentralized messaging (nil if disabled)
	txIndexer          *txindexer.Indexer          // Live tx-hash lookup tiers (nil when the MDBX table owns them)
	eraStore           *ancientera.Store           // Read-only sealed history (nil when no era dir present)
	storageBridge      *dstorage.Bridge            // IPFS/Filecoin storage bridge (nil if disabled)
	storageResolver    *dstorage.UniversalResolver // Multi-protocol content resolver (nil if disabled)
	notifyService      *dnotify.Service            // Push notifications (nil if disabled)
	web3Gateway        *api.Web3Gateway            // web3:// protocol gateway (nil if disabled)
	backfillService    *backfill.Service           // Background history backfill (nil if disabled)
	walletService      *wallet.Service             // AI agent wallet (nil if disabled)
	agentRegistry      *coord.AgentRegistry        // AI agent discovery & coordination (nil if disabled)

	// AI safety infrastructure
	dataGovernance     *governance.Committee           // Data governance (nil if disabled)
	trainingProver     *training.TrainingProver        // ZK training verification (nil if disabled)
	attestationService *attestation.AttestationService // Inference attestation (nil if disabled)
	inferenceExecutor  *inference.WazeroExecutor       // AI inference WASM executor (nil if disabled)

	mobilePacketService *mobileverify.PacketService       // mobile attestation packet distribution (nil if disabled)
	mobileRegistry      *mobileverify.Registry            // mobile BLS registry (nil if disabled)
	mobileCohort        *mobileverify.CohortCoordinator   // block-height cross-node cohort cert merge (nil if disabled)
	mobileCohortRelay   *mobileverify.CohortRelay         // gossip wiring for the cohort merge protocol (nil if disabled)
	mobileCertStore     *mobileverify.CertStore           // attestation certificates (nil if disabled)
	mobileHTTPServer    *mobileverify.HTTPServer          // phone-facing HTTP surface (nil if disabled)
	mobileTorrentClient *dtorrent.Client                  // swarm seeding client for packets (nil if disabled)
	mobileRegService    *mobileverify.RegistrationService // registration gossip replication (nil if disabled)
	mobileCommitter     *mobileverify.EpochCommitter      // epoch accumulator committer (nil if disabled)
	mobileAlarms        *mobileverify.AlarmBuffer         // divergence alarm ring (nil if disabled)

	zkProverService *zkprover.Service    // ZK prover gRPC client (nil if disabled)
	zkVerifier      *zkverifier.Verifier // ZK proof verifier (nil if disabled)

	// Cross-chain bridge (ZK proof + Hyperlane multi-chain)
	bridgeRouter    *bridge.ZKRouter        // Multi-chain bridge router (nil if disabled)
	bridgePublisher *bridge.BridgePublisher // N42→ETH state root publisher (nil if disabled)
	bridgeCancel    context.CancelFunc      // Cancels bridge goroutines on shutdown

	keyDir     string // key store directory
	keyDirTemp bool   // If true, key directory will be removed by Stop

	tracingShutdown func(context.Context) error // flushes and stops the OTel tracer provider

	// Stateless validation mode (validates blocks via JMT witnesses, no full state DB)
	statelessMode      bool
	statelessValidator *stateless.Validator // nil unless statelessMode is enabled

	// OtterSync (BitTorrent-based chain sync via EraE segments)
	torrentsyncService *torrentsync.Service

	// Ingest server for stress testing
	ingestServer *ingest.Server

	// Development tools
	txGenerator *txgen.Generator // Transaction generator for testing
}

const (
	initializingState = iota
	runningState
	closedState
)

// nodeBuild carries the locals NewNode's phases share. Each field keeps the
// name of the NewNode local it replaced; closures created by the phases read
// these fields through b, so they observe later assignments (notably
// chainKv = nil on success) exactly as the original locals did.
type nodeBuild struct {
	cliCtx *cli.Context
	ctx    context.Context
	cancel context.CancelFunc
	cfg    *conf.Config

	// Track resources that need cleanup on failure.
	chainKv kv.RwDB
	// Attached to the Node once it exists, so shutdown can stop its sealer.
	nodeTxIndexer *txindexer.Indexer
	// Read-only sealed-history store, attached for shutdown cleanup.
	nodeEraStore *ancientera.Store
	dirLockNode  Node // tracks dirLock for cleanup

	genesisBlock    block.IBlock
	engine          consensus.Engine
	depositContract *deposit.Deposit
	genesisHash     types.Hash
	genesisConfig   *conf.Genesis
	chainConfig     *params.ChainConfig
	profile         params.ProfileDescriptor

	configuredGenesis  configuredGenesis
	frameKv            kv.RwDB
	p2pGenesisHash     types.Hash
	p2p                *p2p.Service
	bc                 common.IBlockChain
	exexMgr            *exex.Manager
	zkProverSvc        *zkprover.Service
	zkVerify           *zkverifier.Verifier
	pool               common.ITxsPool
	is                 *initialsync.Service
	snapSyncSvc        *snapsync.Service
	checkpointSvc      *checkpoint.Service
	syncServer         *n42sync.Service
	miner              *miner.Miner
	keyDir             string
	isEphem            bool
	accman             *accounts.Manager
	engineStateAdapter *api.EngineStateAdapter
	gpoParams          conf.GpoConfig
}

func NewNode(cliCtx *cli.Context, cfg *conf.Config) (*Node, error) {
	ctx, cancel := context.WithCancel(cliCtx.Context)
	success := false
	b := &nodeBuild{cliCtx: cliCtx, ctx: ctx, cancel: cancel, cfg: cfg}
	n := new(Node)

	defer func() {
		if !success {
			cancel()
			if b.dirLockNode.dirLock != nil {
				b.dirLockNode.dirLock.Unlock()
			}
			if b.chainKv != nil {
				b.chainKv.Close()
			}
		}
	}()

	if err := n.initProfile(b); err != nil {
		return nil, err
	}
	if err := n.initStorageTier(b); err != nil {
		return nil, err
	}
	if err := n.initChainDB(b); err != nil {
		return nil, err
	}
	if err := n.initFrameStore(b); err != nil {
		return nil, err
	}
	if b.frameKv != nil {
		defer func() {
			if !success {
				rawdb.CloseFrameLog()
				rawdb.SetFrameStore(nil)
				b.frameKv.Close()
			}
		}()
	}
	if err := n.initStoredGenesis(b); err != nil {
		return nil, err
	}
	if err := n.initDataDirLock(b); err != nil {
		return nil, err
	}
	if err := n.initGenesis(b); err != nil {
		return nil, err
	}
	if err := n.initChainConfig(b); err != nil {
		return nil, err
	}
	if err := n.initP2P(b); err != nil {
		return nil, err
	}
	if err := n.initBlockChain(b); err != nil {
		return nil, err
	}
	if err := n.initBlockChainFeatures(b); err != nil {
		return nil, err
	}
	if err := n.initExEx(b); err != nil {
		return nil, err
	}
	if err := n.initStateCommitment(b); err != nil {
		return nil, err
	}
	if err := n.initZKProving(b); err != nil {
		return nil, err
	}
	if err := n.initSnapshotAccel(b); err != nil {
		return nil, err
	}
	if err := n.initTxPool(b); err != nil {
		return nil, err
	}
	if err := n.initSyncServices(b); err != nil {
		return nil, err
	}
	if err := n.initMiner(b); err != nil {
		return nil, err
	}
	if err := n.initAccounts(b); err != nil {
		return nil, err
	}
	if err := n.initEngineStateAdapter(b); err != nil {
		return nil, err
	}
	if err := n.initNodeStruct(b); err != nil {
		return nil, err
	}
	if err := n.initBanner(b); err != nil {
		return nil, err
	}
	if err := n.initTracing(b); err != nil {
		return nil, err
	}
	if err := n.initStateless(b); err != nil {
		return nil, err
	}
	if err := n.initAPI(b); err != nil {
		return nil, err
	}
	if err := n.initBundler(b); err != nil {
		return nil, err
	}
	if err := n.initPeerDAS(b); err != nil {
		return nil, err
	}

	success = true
	b.chainKv = nil // prevent deferred cleanup from closing the DB now owned by node
	return n, nil
}

func (n *Node) Profile() params.ProfileDescriptor {
	return n.profile
}

func (n *Node) Start() error {
	n.startStopLock.Lock()
	defer n.startStopLock.Unlock()

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

	// Mobile attestation packet pipeline: wired before the miner starts (the
	// sink is the worker's read-log capture switch), and on non-miner nodes
	// too — every IDC node subscribes and caches the fleet's packets.
	if err := n.startMobileVerify(); err != nil {
		return err
	}

	// Distributed runtime (coprocessor, messaging, etc.) must be constructed
	// before the JSON-RPC API list is assembled below, or a namespace like
	// coprocessor_* — gated on n.coprocessorService != nil — is silently
	// dropped from n.rpcAPIs even though the service itself started fine (it
	// used to run near the end of Start(), well after rpcAPIs was already
	// built and startRPC() had resolved the HTTP module list). Nothing in
	// startDistributedRuntime depends on the miner/consensus/RPC setup that
	// follows — it only reads n.config and (for messaging) the already-ready
	// n.p2p, exactly like startMobileVerify just above.
	n.startDistributedRuntime()
	n.startDDNRuntime()

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

	consensusPlan := resolveConsensusRuntimePlan(n.engine)

	if consensusPlan.bindAPOSBlockChain {
		pos := n.engine.(*apos.APos)
		pos.SetBlockChain(n.blockChain)
	}

	if consensusPlan.startHotStuffService {
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
	}

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

	// Bridge runtime registers bridge_* RPC APIs, so it must run BEFORE the RPC
	// server snapshots n.rpcAPIs (same dead-registration class fixed for
	// coprocessor). All its dependencies — blockChain, engine validator set,
	// etherbase, zkprover — are ready by here. Off by default (BridgeCfg.Enabled).
	n.startBridgeRuntime()

	if err := n.startRPC(); err != nil {
		log.Error("failed start jsonrpc service", zap.Error(err))
		return err
	}

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

	// Start PeerDAS service if enabled
	if n.peerdasService != nil {
		if err := n.peerdasService.Start(n.ctx); err != nil {
			log.Warn("PeerDAS service failed to start", "err", err)
		}
	}

	n.runtimePlan.logProfileDisabled(n.profile)
	n.startMCPServer()

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

	// Start ZK prover service if configured.
	if n.zkProverService != nil {
		n.zkProverService.Start()
	}

	n.startAIRuntime()
	n.startWeb3Gateway()
	n.startIngestServer()
	// startBridgeRuntime moved above startRPC so its bridge_* APIs register.

	if n.runtimePlan.startTxGenerator {
		n.startTxGenerator()
	}

	log.PrintSuccess("All services started")

	return nil
}

// InstanceDir retrieves the instance directory used by the protocol stack.
func (n *Node) InstanceDir() string {
	return n.config.NodeCfg.DataDir
}

func (n *Node) Close() error {
	n.startStopLock.Lock()
	defer n.startStopLock.Unlock()

	n.lock.Lock()
	state := n.state
	n.lock.Unlock()
	switch state {
	case initializingState:
		// The node was never started.
		return n.doClose(nil)
	case runningState:
		// The node was started, release resources acquired by Start().
		var errs []error
		if err := n.stopServices(); err != nil {
			errs = append(errs, err...)
		}
		return n.doClose(errs)
	case closedState:
		return ErrNodeStopped
	default:
		return fmt.Errorf("node is in unknown state %d", state)
	}
}

// namedCloser pairs a human-readable service name with its shutdown function.
// A nil return from closer indicates the service stopped without error.
type namedCloser struct {
	name   string
	closer func() error
}

// stopServices terminates running services, RPC and p2p networking.
// It is the inverse of Start. Services are stopped in dependency order:
// consumers first, then infrastructure layers (blockchain, P2P) last.
func (n *Node) stopServices() []error {
	// Cancel the node context first so all background goroutines receive
	// the shutdown signal before we begin stopping services sequentially.
	if n.cancel != nil {
		n.cancel()
	}

	var errs []error

	services := []namedCloser{
		// 1. RPC services (depends on everything, stop first)
		{"RPC services", func() error { n.stopRPC(); return nil }},
		// 2a. gRPC KV server
		{"gRPC KV server", func() error {
			if n.grpcServer != nil {
				n.grpcServer.GracefulStop()
			}
			return nil
		}},
		// 2b. web3:// gateway
		{"web3 gateway", func() error {
			if n.web3Gateway != nil {
				n.web3Gateway.Stop()
			}
			return nil
		}},
		// 2c. AI services
		{"AI wallet", func() error {
			n.walletService = nil
			return nil
		}},
		{"AI safety + coord", func() error {
			n.attestationService = nil
			n.trainingProver = nil
			n.dataGovernance = nil
			n.agentRegistry = nil
			return nil
		}},
		{"AI inference", func() error {
			if n.inferenceExecutor != nil {
				if err := n.inferenceExecutor.Close(context.Background()); err != nil {
					log.Warn("AI inference executor close failed", "err", err)
				}
				n.inferenceExecutor = nil
			}
			return nil
		}},
		{"Mobile attestation", func() error {
			if n.mobileHTTPServer != nil {
				n.mobileHTTPServer.Stop()
				n.mobileHTTPServer = nil
			}
			if n.mobileRegService != nil {
				n.mobileRegService.Stop()
				n.mobileRegService = nil
			}
			if n.mobileCommitter != nil {
				n.mobileCommitter.Stop() // final commit drains pending
				n.mobileCommitter = nil
			}
			if n.mobileCohortRelay != nil {
				n.mobileCohortRelay.Stop()
				n.mobileCohortRelay = nil
			}
			if n.mobileCohort != nil {
				n.mobileCohort.Stop()
				n.mobileCohort = nil
			}
			if n.mobilePacketService != nil {
				n.mobilePacketService.Stop()
				n.mobilePacketService = nil
			}
			if n.mobileTorrentClient != nil {
				n.mobileTorrentClient.Stop()
				n.mobileTorrentClient = nil
			}
			return nil
		}},
		// 2d. Cross-chain bridge
		{"Bridge", func() error {
			if n.bridgeCancel != nil {
				n.bridgeCancel()
			}
			if n.bridgeRouter != nil {
				n.bridgeRouter.Close()
				n.bridgeRouter = nil
			}
			n.bridgePublisher = nil
			return nil
		}},
		// 2e. Distributed infrastructure
		{"Distributed services", func() error {
			if n.ddnGateway != nil {
				n.ddnGateway.Stop()
			}
			if n.ddnSigner != nil {
				n.ddnSigner.Close()
			}
			// Before the database closes: the sealer may be mid-write.
			if n.txIndexer != nil {
				n.txIndexer.Stop()
			}
			// NOTE: the era store is closed in its own late phase (after
			// Sync/P2P) — RPC and range-serving goroutines read it until
			// those services stop.
			if n.notifyService != nil {
				n.notifyService.Stop()
			}
			if n.messagingService != nil {
				n.messagingService.Stop()
			}
			n.storageResolver = nil
			if n.storageBridge != nil {
				n.storageBridge.Stop()
			}
			if n.coprocessorService != nil {
				n.coprocessorService.Stop()
			}
			return nil
		}},
		// 2e. OtterSync (stateless — just nil the reference)
		{"OtterSync", func() error {
			n.torrentsyncService = nil
			return nil
		}},
		// 2c. Deferred execution pipeline
		{"Deferred execution", func() error {
			if n.deferredPipeline != nil {
				n.deferredPipeline.Stop()
			}
			return nil
		}},
		// 2. Snapshot manager
		{"Snapshot manager", func() error {
			if n.snapshotMgr != nil {
				n.snapshotMgr.Stop()
			}
			return nil
		}},
		// 3. Pruner
		{"Pruner", func() error {
			if n.pruner != nil {
				n.pruner.Stop()
			}
			return nil
		}},
		// 3a. History backfiller — stopped before the DB closes; it holds no
		// state of its own, and a partial batch is simply rebuilt from the
		// marker, so an abrupt stop costs one batch of work and nothing else.
		{"History backfiller", func() error {
			if n.historyBackfiller != nil {
				n.historyBackfiller.Stop()
			}
			return nil
		}},
		// 3b. History expirer
		{"History expirer", func() error {
			if n.historyExpirer != nil {
				n.historyExpirer.Stop()
			}
			return nil
		}},
		// 3c. Ingest server
		{"Ingest server", func() error {
			if n.ingestServer != nil {
				n.ingestServer.Stop()
			}
			return nil
		}},
		// 3d. Transaction generator
		{"Transaction generator", func() error {
			if n.txGenerator != nil {
				n.txGenerator.Stop()
			}
			return nil
		}},
		// 3d. Bundler service
		{"Bundler service", func() error {
			if n.bundlerService != nil {
				n.bundlerService.Stop()
			}
			return nil
		}},
		// 3e. PeerDAS service
		{"PeerDAS service", func() error {
			if n.peerdasService != nil {
				return n.peerdasService.Stop()
			}
			return nil
		}},
		// 3f. MCP server
		{"MCP server", func() error {
			if n.mcpServer != nil {
				return n.mcpServer.Stop()
			}
			return nil
		}},
		{"ZK prover", func() error {
			if n.zkProverService != nil {
				n.zkProverService.Stop()
			}
			return nil
		}},
		// 4. Miner
		{"Miner", func() error { n.miner.Close(); return nil }},
		// 4b. Backfill service (stop before sync services)
		{"Backfill sync", func() error {
			if n.backfillService != nil {
				n.backfillService.Stop()
			}
			return nil
		}},
		// 5. Checkpoint sync + Snap sync + Initial sync
		// Note: checkpointSync relies on node context cancellation (done above).
		{"Checkpoint sync", func() error {
			// Cancellation owns checkpoint shutdown; keep the pointer immutable
			// while the sync-startup goroutine observes it.
			return nil
		}},
		{"Snap sync", func() error { return n.snapSync.Stop() }},
		{"Initial sync", func() error { return n.is.Stop() }},
		// 6. Sync service (depends on P2P + blockchain, must stop before blockchain closes)
		{"Sync service", func() error { return n.sync.Stop() }},
		// 7. Transaction pool
		{"Transaction pool", func() error { return n.txspool.Stop() }},
		// 8. Deposit contract
		{"Deposit contract", func() error {
			if n.depositContract != nil {
				return n.depositContract.Stop()
			}
			return nil
		}},
		// 8b. HotStuff consensus service
		{"HotStuff service", func() error {
			if n.hotstuffService != nil {
				n.hotstuffService.Stop()
			}
			if n.hotstuffJournalDB != nil {
				n.hotstuffJournalDB.Close()
			}
			return nil
		}},
		// 9. Consensus engine
		{"Consensus engine", func() error { return n.engine.Close() }},
		// 10. ExEx manager (stop extensions before blockchain closes)
		{"ExEx manager", func() error {
			if n.exexManager != nil {
				n.exexManager.Stop()
			}
			return nil
		}},
		// 10b. Snapshot journal (persist diff layers before blockchain closes)
		{"Snapshot journal", func() error {
			if realBC, ok := n.blockChain.(*internal.BlockChain); ok {
				if tree := realBC.SnapshotTree(); tree != nil {
					if err := tree.SaveJournal(); err != nil {
						log.Warn("Failed to save snapshot journal", "err", err)
					}
				}
			}
			return nil
		}},
		// 10c. Verkle tree flush (persist nodes before blockchain closes)
		{"Verkle flush", func() error {
			realBC, ok := n.blockChain.(*internal.BlockChain)
			if !ok {
				return nil
			}
			vc := realBC.VerkleCommitment()
			if vc == nil {
				return nil
			}
			nodes, bytes, err := vc.Flush()
			if err != nil {
				log.Warn("Failed to flush Verkle tree", "err", err)
			} else if nodes > 0 {
				log.Info("Verkle tree flushed on shutdown", "nodes", nodes, "bytes", bytes)
			}
			return nil
		}},
		// 10d. Release references to stateless helpers (no goroutines to stop)
		{"ZK verifier + stateless", func() error {
			n.zkVerifier = nil
			n.statelessValidator = nil
			return nil
		}},
		// 11. Blockchain (flush and close DB, after all consumers stopped)
		{"Blockchain", func() error { return n.blockChain.Close() }},
		// 11. OpenTelemetry tracing (flush remaining spans before network goes down)
		{"Tracing", func() error {
			if n.tracingShutdown != nil {
				return n.tracingShutdown(context.Background())
			}
			return nil
		}},
		// 12. P2P networking (transport layer, last to go)
		{"Ethereum devp2p", func() error {
			if n.devp2pServer != nil {
				n.devp2pServer.Stop()
				n.devp2pServer = nil
			}
			return nil
		}},
		{"P2P network", func() error { return n.p2p.Stop() }},
		// Era store LAST among readers' dependencies: sync/P2P/RPC serve
		// ancient reads until stopped; closing earlier would turn their
		// in-flight reads into spurious errors.
		{"Era store", func() error {
			if n.eraStore != nil {
				rawdb.SetAncientStore(nil)
				n.eraStore.Close()
				n.eraStore = nil
			}
			return nil
		}},
	}

	for i, svc := range services {
		log.PrintShutdownStep(i+1, len(services), svc.name)
		if err := svc.closer(); err != nil {
			errs = append(errs, err)
			log.PrintSubItem(fmt.Sprintf("%s stopped with error: %v", svc.name, err))
		}
	}
	if commitment.QMDBStateReadMode() == commitment.QMDBReadVerify {
		accounts, storage, compared := commitment.QMDBVerifyTotals()
		log.Info("qmdb verify final", "compared", compared, "accountMismatch", accounts, "storageMismatch", storage)
	}

	return errs
}

func (n *Node) Wait() {
	<-n.shutDown
}

// AccountManager retrieves the account manager used by the protocol stack.
func (n *Node) AccountManager() *accounts.Manager {
	return n.accman
}

// BlockChain returns the blockchain instance managed by this node.
func (n *Node) BlockChain() common.IBlockChain {
	return n.blockChain
}

func (n *Node) Database() kv.RwDB {
	return n.db
}

func (n *Node) Miner() common.IMiner {
	return n.miner
}

func (n *Node) Engine() consensus.Engine {
	return n.engine
}

func (n *Node) ChainDb() kv.RwDB {
	return n.db
}

func init() {
	envswitch.Declare("N42_FRAME_LOG_KEEP_SEGMENTS", envswitch.Diag, "Segments the frame log keeps.")
	envswitch.Declare("N42_TXINDEX_DIR", envswitch.Config, "Cold tx-hash to block index directory (txlookup segments).")
	envswitch.Declare("N42_BLOCKHASH_DIR", envswitch.Config, "Cold blockHash to number index directory (blockhashindex segments).")
	envswitch.Declare("N42_SEPARATE_VOTE_DB", envswitch.Config, "Keep votes in their own database.")
	envswitch.Declare("N42_FRAME_DB", envswitch.Config, "Frames go to their own MDBX store instead of the chain database.")
	envswitch.Declare("N42_FRAME_LOG", envswitch.Diag, "Append frame data to a log beside the frame store.")
	envswitch.Declare("N42_FRAME_DB_SYNC", envswitch.BenchOnly, "BENCH ONLY: safe-nosync drops the frame store's per-commit fsync.")
	envswitch.Declare("N42_FRAME_DB_MAPSIZE_GB", envswitch.Perf, "Map size of the frame store, in GB.")
	envswitch.Declare("N42_MDBX_SYNC", envswitch.Config, "MDBX durability mode.")
	envswitch.Declare("N42_MDBX_DIRTY_GB", envswitch.Perf, "Dirty-page limit of the ChainDB, in GB.")
	envswitch.Declare("N42_MDBX_MAPSIZE_GB", envswitch.Config, "Map size of the ChainDB, in GB.")
	envswitch.Declare("N42_HISTORY_INDEX_INTERVAL", envswitch.Config, "How often the deferred history backfiller folds the changesets.")
	envswitch.Declare(attest.EnvFlushMs, envswitch.Perf, "How long a committee member batches attestations before signing, in ms.")
}
