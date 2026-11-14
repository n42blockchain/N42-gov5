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
	"fmt"
	"sync"

	"github.com/gofrs/flock"
	"github.com/urfave/cli/v2"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/envswitch"
	"github.com/n42blockchain/N42/common/types"
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
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	ddngateway "github.com/n42blockchain/N42/internal/ddn/gateway"
	ddnreceipt "github.com/n42blockchain/N42/internal/ddn/receipt"
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
	"github.com/n42blockchain/N42/internal/txgen"
	"github.com/n42blockchain/N42/internal/txindexer"
	"github.com/n42blockchain/N42/internal/zkprover"
	"github.com/n42blockchain/N42/internal/zkverifier"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rawdb/ancientera"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state/commitment"
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

	if err := n.startPreflight(); err != nil {
		return err
	}
	if err := n.startBlockChain(); err != nil {
		return err
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

	if err := n.startMining(); err != nil {
		return err
	}

	consensusPlan := resolveConsensusRuntimePlan(n.engine)
	if err := n.startConsensus(consensusPlan); err != nil {
		return err
	}
	if err := n.startRPCAPIs(consensusPlan); err != nil {
		return err
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

	if err := n.startNetworkingAndSync(); err != nil {
		return err
	}
	n.startTorrentSync()
	n.startSyncPipeline()
	n.startCacheWarmers()
	n.startStorageMaintenance()
	n.startPeerDAS()

	n.runtimePlan.logProfileDisabled(n.profile)
	n.startMCPServer()
	if err := n.startGRPCKVServer(); err != nil {
		return err
	}
	n.startDeferredPipeline()
	n.startZKProver()

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

	services := n.stopRPCClosers()
	services = append(services, n.stopAIClosers()...)
	services = append(services, n.stopDistributedClosers()...)
	services = append(services, n.stopAuxiliaryClosers()...)
	services = append(services, n.stopChainClosers()...)
	services = append(services, n.stopStateClosers()...)
	services = append(services, n.stopNetworkClosers()...)

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
