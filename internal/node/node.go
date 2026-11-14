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
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	gverkle "github.com/ethereum/go-verkle"
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
	"github.com/n42blockchain/N42/internal/blockhashindex"
	"github.com/n42blockchain/N42/internal/bridge"
	"github.com/n42blockchain/N42/internal/bundler"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/cs"
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
	"github.com/n42blockchain/N42/internal/exex/extensions"
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
	"github.com/n42blockchain/N42/internal/tracing"
	"github.com/n42blockchain/N42/internal/txgen"
	"github.com/n42blockchain/N42/internal/txindexer"
	"github.com/n42blockchain/N42/internal/txlookup"
	"github.com/n42blockchain/N42/internal/txspool"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/zkprover"
	"github.com/n42blockchain/N42/internal/zkverifier"
	"github.com/n42blockchain/N42/lib/bmt"
	bmtstore "github.com/n42blockchain/N42/lib/bmt/store"
	"github.com/n42blockchain/N42/lib/common/datadir"
	"github.com/n42blockchain/N42/lib/gointerfaces/remote"
	"github.com/n42blockchain/N42/lib/jmt"
	jmtstore "github.com/n42blockchain/N42/lib/jmt/store"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/lib/kv/remotedbserver"
	log3 "github.com/n42blockchain/N42/lib/log/v3"
	libverkle "github.com/n42blockchain/N42/lib/verkle"
	verklestore "github.com/n42blockchain/N42/lib/verkle/store"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rawdb/ancientera"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
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

	p2pGenesisHash     types.Hash                  // genesis hash used for P2P fork digest
	exexManager        *exex.Manager               // Execution Extensions manager
	hotstuffService    *hotstuff.Service           // HotStuff BFT consensus service (nil if not using HotStuff)
	hotstuffJournalDB  kv.RwDB                     // separate vote journal store (nil unless enabled)
	frameDB            kv.RwDB                     // frame store (nil unless N42_FRAME_DB)
	bundlerService     *bundler.BundlerService     // ERC-4337 bundler service (nil if disabled)
	peerdasService     *peerdas.Service            // PeerDAS (EIP-7594) data availability service (nil if disabled)
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

func NewNode(cliCtx *cli.Context, cfg *conf.Config) (*Node, error) {
	ctx, cancel := context.WithCancel(cliCtx.Context)
	success := false

	// Track resources that need cleanup on failure.
	var chainKv kv.RwDB
	// Attached to the Node once it exists, so shutdown can stop its sealer.
	var nodeTxIndexer *txindexer.Indexer
	// Read-only sealed-history store, attached for shutdown cleanup.
	var nodeEraStore *ancientera.Store
	var dirLockNode Node // tracks dirLock for cleanup

	defer func() {
		if !success {
			cancel()
			if dirLockNode.dirLock != nil {
				dirLockNode.dirLock.Unlock()
			}
			if chainKv != nil {
				chainKv.Close()
			}
		}
	}()

	var (
		genesisBlock    block.IBlock
		node            Node
		engine          consensus.Engine
		depositContract *deposit.Deposit
		genesisHash     types.Hash
		genesisConfig   *conf.Genesis
		chainConfig     *params.ChainConfig
		profile         params.ProfileDescriptor
		err             error
	)

	profile, err = params.ResolveExecutionProfile(cfg.NodeCfg.Profile)
	if err != nil {
		return nil, err
	}
	cfg.NodeCfg.Profile = profile.String()

	configuredGenesis, err := resolveConfiguredGenesis(cfg, profile)
	if err != nil {
		return nil, err
	}

	// Apply storage tier configuration before opening any databases.
	if cfg.StorageTierCfg.Enabled {
		dirs := datadir.New(cfg.NodeCfg.DataDir)
		if err := applyStorageTier(&cfg.StorageTierCfg, &dirs); err != nil {
			return nil, fmt.Errorf("storage tier: %w", err)
		}
	}

	chainKv, err = OpenDatabase(ctx, cfg, nil, kv.ChainDB.String())
	if err != nil {
		return nil, err
	}
	frameKv, err := openFrameStoreDB(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if frameKv != nil {
		rawdb.SetFrameStore(frameKv)
		if frameLogEnabled() {
			// N42_FRAME_LOG=1: frame data goes to an append-only log in
			// <datadir>/framelog; the frame store keeps its index.
			keep := 0
			if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("N42_FRAME_LOG_KEEP_SEGMENTS"))); err == nil && n > 0 {
				keep = n
				log.Warn("!!! FRAME LOG RETENTION ON (BENCH ONLY) !!! N42_FRAME_LOG_KEEP_SEGMENTS: older segments are "+
					"deleted; this node cannot serve or re-read older blocks", "keepSegments", n)
			}
			dir := filepath.Join(cfg.NodeCfg.DataDir, "framelog")
			if _, err := rawdb.OpenFrameLog(dir, keep); err != nil {
				rawdb.SetFrameStore(nil)
				frameKv.Close()
				return nil, fmt.Errorf("opening the frame log: %w", err)
			}
			log.Info("Frame log open (N42_FRAME_LOG)", "path", dir)
		}
		defer func() {
			if !success {
				rawdb.CloseFrameLog()
				rawdb.SetFrameStore(nil)
				frameKv.Close()
			}
		}()
	}

	if err := chainKv.View(ctx, func(tx kv.Tx) error {
		// Chains converted with --virtual-td carry a DatabaseInfo marker instead
		// of per-block all-zero TD rows; ReadTd then synthesizes TD 0 for known
		// headers (and keeps nil => ErrUnknownAncestor for unknown ones).
		rawdb.SetupVirtualTdFromDB(tx)
		genesisHash, err = rawdb.ReadCanonicalHash(tx, 0)
		if genesisHash == (types.Hash{}) && err != nil {
			return internal.ErrGenesisNoConfig
		}
		if genesisHash == (types.Hash{}) {
			// No genesis stored yet; caller will write it below.
			return nil
		}

		chainConfig, err = rawdb.ReadChainConfig(tx, genesisHash)
		if err != nil {
			return err
		}

		if genesisBlock, err = rawdb.ReadBlockByHash(tx, genesisHash); genesisBlock == nil {
			return fmt.Errorf("genesisBlock is missing err:%w", err)
		}

		return nil
	}); err != nil {
		return nil, err
	}

	// Built-in chains: the COMPILED chainspec is authoritative (erigon model) —
	// parameter and fork-schedule upgrades ship with the binary, and every node
	// of the network runs the same build. The stored copy remains authoritative
	// only for private/unknown chains (it is the sole record of their config).
	// Preferring the stored copy for built-ins silently froze every chainspec
	// change made after a datadir was seeded — observed live: a hotstuff
	// parameter added to the spec parsed correctly but never took effect,
	// because the copy written at seeding time kept winning on every start.
	if chainConfig != nil && configuredGenesis.chainConfig != nil && !configuredGenesis.isPrivate {
		chainConfig = configuredGenesis.chainConfig
	}

	// Acquire the instance directory lock.
	if err := node.openDataDir(cfg); err != nil {
		return nil, err
	}
	dirLockNode.dirLock = node.dirLock // track for cleanup on error

	if genesisHash == (types.Hash{}) {
		if err := ValidateDataDirNetworkBinding(cfg, configuredGenesis.chainConfig, configuredGenesis.genesisHash); err != nil {
			return nil, err
		}
		if configuredGenesis.requiresExplicitInit {
			return nil, explicitInitRequiredError(cfg.NodeCfg.Chain, cfg.NodeCfg.DataDir)
		}

		genesisConfig = configuredGenesis.genesis
		chainConfig = configuredGenesis.chainConfig
		if !configuredGenesis.isPrivate {
			genesisHash = *configuredGenesis.genesisHash
		}
		if err := chainKv.Update(ctx, func(tx kv.RwTx) error {
			var writeErr error
			genesisBlock, writeErr = WriteGenesisBlock(tx, genesisConfig)
			if writeErr != nil {
				return writeErr
			}
			if configuredGenesis.isPrivate {
				genesisHash = genesisBlock.Hash()
			}
			return nil
		}); err != nil {
			return nil, err
		}
		if err := PersistDataDirNetworkBinding(cfg, chainConfig, genesisHash); err != nil {
			return nil, err
		}
		if profile.IsEthereumEL() && genesisConfig != nil {
			ethGenesisHash, hashErr := internal.EthereumCompatibleGenesisHash(genesisConfig)
			if hashErr != nil {
				return nil, hashErr
			}
			if err := PersistEthereumGenesisHash(cfg.NodeCfg.DataDir, ethGenesisHash); err != nil {
				return nil, err
			}
		}
	} else {
		if err := ensureDataDirNetworkBinding(cfg, chainConfig, genesisHash); err != nil {
			return nil, err
		}
	}

	// Enforce StateScheme immutability: once written to the DB, it cannot change.
	if chainConfig != nil && chainConfig.StateScheme != "" {
		wantScheme := configuredGenesis.chainConfig.StateScheme
		if wantScheme == "" {
			wantScheme = "legacy-keccak"
		}
		if chainConfig.StateScheme != wantScheme {
			return nil, fmt.Errorf("state scheme mismatch: db has %q, config wants %q — "+
				"changing the state commitment algorithm after genesis is not allowed",
				chainConfig.StateScheme, wantScheme)
		}
	}

	// Enforce StateScheme immutability: once written to the DB, it cannot change.
	if chainConfig != nil && chainConfig.StateScheme != "" {
		wantScheme := configuredGenesis.chainConfig.StateScheme
		if wantScheme == "" {
			wantScheme = "legacy-keccak"
		}
		if chainConfig.StateScheme != wantScheme {
			return nil, fmt.Errorf("state scheme mismatch: db has %q, config wants %q — "+
				"changing the state commitment algorithm after genesis is not allowed",
				chainConfig.StateScheme, wantScheme)
		}
	}

	// Update ChainConfig on every startup for non-private chains.
	if !configuredGenesis.isPrivate {
		if err := chainKv.Update(ctx, func(tx kv.RwTx) error {
			genesisHash = *configuredGenesis.genesisHash
			genesisConfig = configuredGenesis.genesis
			if genesisConfig != nil {
				return WriteChainConfig(tx, genesisHash, genesisConfig)
			}
			if err := rawdb.WriteChainConfig(tx, genesisHash, configuredGenesis.chainConfig); err != nil {
				log.Error("failed to write chain config", "err", err)
				return err
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}

	cfg.ChainCfg = chainConfig

	// Use the hardcoded genesis hash for P2P fork digest when a known chain is
	// configured. The actual genesis block hash may differ due to Header struct
	// evolution (e.g., added blob gas fields), but the chain data is identical.
	// This ensures fork digest compatibility with peers running older versions.
	p2pGenesisHash := genesisBlock.Hash()
	if canonicalHash, ok := configuredGenesis.canonicalHash(); ok {
		p2pGenesisHash = canonicalHash
	}
	// An explicit operator override wins over both the computed and the hardcoded
	// hash. Required to sync a deployed network whose on-wire genesis differs from
	// this binary's constant (see --p2p.genesis-override).
	if ov := strings.TrimSpace(cfg.P2PCfg.GenesisHashOverride); ov != "" {
		overrideHash := types.HexToHash(ov)
		log.Warn("Overriding P2P genesis hash for fork digest",
			"override", overrideHash.Hex(),
			"configured", p2pGenesisHash.Hex())
		p2pGenesisHash = overrideHash
	}
	p2p, err := p2p.NewService(ctx, p2pGenesisHash, cfg.P2PCfg, cfg.NodeCfg)
	if err != nil {
		return nil, err
	}

	engine, err = resolveConsensusEngine(cfg, profile, chainKv)
	if err != nil {
		return nil, err
	}

	bc, err := internal.NewBlockChain(ctx, genesisBlock, engine, chainKv, p2p, cfg.ChainCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create blockchain: %w", err)
	}

	// Initialize cross-block EVM code analysis cache (AIP-107 equivalent).
	// Caches codeBitmap() results across blocks, avoiding O(n) re-analysis
	// for contracts already seen. ~2x EVM execution speedup.
	vm2.GlobalCodeAnalysisCache = vm2.NewCodeAnalysisCache(8192)

	// Enable parallel EVM execution, state prefetching, and ancient DB if configured.
	if realBC, ok := bc.(*internal.BlockChain); ok {
		if cfg.NodeCfg.ParallelEVM {
			// EXPERIMENTAL. Off by default; only reachable via the config key,
			// cmd/n42 exposes no CLI flag for it.
			//
			// This warning used to assert that internal/parallel does NOT
			// consult the SELFDESTRUCT/CREATE2 storage-wipe set, so an
			// intra-block read of a slot on an account destroyed earlier in the
			// same block returned a stale pre-destruct value. That defect was
			// fixed and the warning was not updated. The wipe shadow-read is
			// present on all three sides: ParallelStateWriter.CreateContract
			// records a FieldStorageWipe marker (IntraBlockState calls it on
			// every SELFDESTRUCT, recreate-after-destruct and fresh CREATE),
			// ParallelStateReader.ReadAccountStorage shadows stale pre-wipe
			// slots with the version comparison, parallel_processor.go applies
			// the wipes on merge, and internal/parallel/wipe_shadow_test.go
			// pins the ordering cases. docs/ethel/erigon-borrow-audit.md
			// records the same conclusion.
			//
			// That defect being closed was never an assessment of the rest of
			// the path, and on 2026-09-02 the rest of the path was measured:
			// an A-B-A on the 7-node bench fleet (sequential / Block-STM /
			// sequential, one binary, the flag the only variable) HALTED THE
			// CHAIN on the middle leg. Six of seven nodes rejected the same two
			// blocks and the measurement window produced zero blocks, while
			// both sequential legs ran clean at ~36,500 TPS with zero bad
			// blocks and agreed with each other to 0.5% on execution cost.
			//
			// The failure is not a state root mismatch and not a storage-wipe
			// case:
			//
			//   could not apply tx 0 from block 13618674: insufficient funds
			//   for gas * price + value: address 0x8AD4..a6Db have 0 want
			//   210000000000001
			//
			// ELEVEN OF THE TWELVE rejections were on tx 0 -- the block's FIRST
			// transaction -- so no earlier transaction of the same block could
			// have credited the sender, and the balance must come from the
			// parent state. The cause is one level lower: ProcessParallel hands
			// every Block-STM worker the SAME state reader, noting only that it
			// "must be safe for concurrent reads". It is not. That reader is a
			// PlainStateReader over an MdbxTx, and MdbxTx.GetOne takes a
			// per-bucket cached cursor out of an unsynchronised map and calls
			// SeekExact on it, so every worker shares one MDBX cursor. The race
			// detector confirms it -- see TestGetOneIsNotConcurrencySafe in
			// lib/kv/mdbx, and docs/QS_TPS_BENCHMARK.md for the round.
			//
			// So this is not "unaudited", it is "known to diverge under load",
			// and the fix is not in internal/parallel: a parallel executor
			// needs a reader per worker, or reads serialised.
			log.Warn("ParallelEVM ENABLED: each Block-STM worker now owns its read transaction and reads the live QMDB tree under its lock (the 2026-09-02 halt was a shared MDBX cursor, 3709ca6a); UNMEASURED under load until a round says otherwise -- do not enable on a chain whose blocks matter",
				"workers", internal.ParallelWorkers())
			realBC.SetParallelEVM(true)
		}
		if cfg.NodeCfg.Prefetch {
			// Same defect as ParallelEVM above, in a feature that is not marked
			// experimental: StatePrefetcher.Prefetch runs NumCPU/2 goroutines
			// against the SAME state reader the executor is using, and that
			// reader is a PlainStateReader over one kv.Tx whose GetOne shares a
			// cached MDBX cursor. See internal/prefetcher.go for the analysis
			// and lib/kv/mdbx TestGetOneIsNotConcurrencySafe for the proof.
			//
			// Refuse outright when there is no shared cache to warm. Prefetch's
			// whole benefit path is that its reads populate the ShardedCache
			// the executor then reads through, and evmRecord wraps the reader
			// in a CachedStateReader only when layered.ExtractCache returns
			// non-nil -- which happens solely for a *layered.LayeredDB.
			// LayeredDBCfg.Enable defaults false, so on a normal datadir the
			// workers race the executor's cursor to fill a cache that does not
			// exist. A warning is the wrong response to a flag that can only
			// cost and never pay: fail fast, the way the mobileverify guard
			// does, and say which config key would make it coherent.
			if err := checkPrefetchHasCache(layered.ExtractCache(chainKv)); err != nil {
				return nil, err
			}
			log.Warn("Prefetch ENABLED — its I/O workers share one state reader with the executor, which races on a single MDBX cursor (see internal/prefetcher.go). Not safe on a chain whose blocks matter")
			realBC.SetPrefetch(true)
			// Enable predictive slot prefetching alongside standard prefetching.
			realBC.SetPrefetchPredictor(internal.NewPrefetchPredictor(64))
		}
		// Transaction-lookup index for the newest blocks. Attached only if it
		// actually comes up: a chain with no indexer keeps writing the MDBX
		// table, whereas a chain that stopped writing it and has no working
		// indexer would leave its transactions findable nowhere.
		ix := txindexer.New(ctx, realBC, chainKv, filepath.Join(cfg.NodeCfg.DataDir, "txindex"))
		if ix.Start() {
			realBC.SetTxIndexer(ix)
			nodeTxIndexer = ix
		}
		// Era store (sealed ancient history, read-only). Auto-detected at
		// <datadir>/ancient-era; produced offline by the seal pipeline —
		// the node never writes it. Sealed ranges are served through the
		// rawdb fallback; damaged optional files degrade to pruned.
		if eraDir := ancientera.DefaultDir(cfg.NodeCfg.DataDir); ancientera.Exists(eraDir) {
			eraStore, health, err := ancientera.OpenStore(eraDir)
			if err != nil {
				return nil, fmt.Errorf("open era store %s: %w", eraDir, err)
			}
			for _, d := range health.Degraded {
				log.Warn("era store: optional range degraded to pruned", "range", d)
			}
			for _, g := range health.ChainGaps {
				log.Error("era store: CRITICAL chain-class damage — restore this era from a peer", "range", g)
			}
			// Cross-check the manifest anchor written by the seal pipeline.
			if err := chainKv.View(ctx, func(tx kv.Tx) error {
				anchor, aerr := tx.GetOne(modules.DatabaseInfo, []byte("eraManifestBlake3"))
				if aerr != nil || len(anchor) != 32 {
					return nil // no anchor recorded — informational only
				}
				mh, merr := ancientera.ManifestHash(eraDir)
				if merr == nil && !bytes.Equal(anchor, mh[:]) {
					log.Warn("era store: manifest differs from the anchored hash (rebuilt or edited manifest)")
				}
				return nil
			}); err != nil {
				return nil, err
			}
			rawdb.SetAncientStore(eraStore)
			nodeEraStore = eraStore
			log.Info("era store attached", "dir", eraDir,
				"sealedEnd", health.SealedEnd, "sealed", health.Sealed,
				"pruned", health.Pruned, "degraded", len(health.Degraded))
		}
		if cfg.NodeCfg.AncientDB {
			ancientBase := cfg.NodeCfg.DataDir
			if cfg.StorageTierCfg.Enabled && cfg.StorageTierCfg.ColdPath != "" {
				ancientBase = cfg.StorageTierCfg.ColdPath
			}
			ancientPath := filepath.Join(ancientBase, "ancient")
			threshold := cfg.NodeCfg.AncientFreezeThreshold
			f, err := freezer.New(ancientPath, threshold)
			if err != nil {
				log.Warn("Failed to open ancient DB, continuing without freezer", "err", err)
			} else {
				realBC.SetFreezer(f)
				// Start background freeze goroutine.
				f.StartFreeze(ctx, func() uint64 {
					cur := realBC.CurrentBlock()
					if cur == nil {
						return 0
					}
					return blockNumberOrZero(cur)
				}, func(start, count uint64) (*freezer.FreezeData, error) {
					var data *freezer.FreezeData
					err := chainKv.View(ctx, func(tx kv.Tx) error {
						var e error
						data, e = rawdb.CollectFreezeData(tx, start, count)
						return e
					})
					return data, err
				}, func(start, count uint64) error {
					return chainKv.Update(ctx, func(tx kv.RwTx) error {
						return rawdb.CleanupFrozenData(tx, start, count)
					})
				})
			}
		}
	}

	// Create and attach the Execution Extensions (ExEx) manager.
	exexMgr := exex.NewManager(0)
	exexMgr.Register(&extensions.LogExtension{})
	if realBC, ok := bc.(*internal.BlockChain); ok {
		realBC.SetExExManager(exexMgr)
	}

	// Initialize state commitment tree based on ChainConfig.StateScheme.
	// Falls back to NodeConfig.JMTCommitment for backward compatibility.
	stateScheme := state.DefaultScheme(chainConfig.StateScheme)
	if stateScheme == state.RootSchemeLegacyKeccak && cfg.NodeCfg.JMTCommitment {
		stateScheme = state.RootSchemeJMTBlake3 // backward compat
	}

	if realBC, ok := bc.(*internal.BlockChain); ok {
		switch stateScheme {
		case state.RootSchemeJMTBlake3:
			// --- JMT initialization (existing production code) ---
			var jmtRoot jmt.Hash
			var jmtVersion uint64
			if rtx, err := chainKv.BeginRo(ctx); err == nil {
				jmtRoot, _ = jmtstore.ReadJMTRoot(rtx)
				jmtVersion, _ = jmtstore.ReadJMTVersion(rtx)
				rtx.Rollback()
			}
			pooledStore := jmtstore.NewPooledDBStore(ctx, chainKv, modules.JMTNode)
			var tree *jmt.Tree
			if jmtRoot == jmt.EmptyHash {
				tree = jmt.New(pooledStore)
			} else {
				tree = jmt.NewFromRoot(pooledStore, jmtRoot)
			}
			tree.SetVersion(jmtVersion)
			jmtCommit := commitment.NewJMTCommitment(tree)
			realBC.SetJMTCommitment(jmtCommit)
			realBC.SetJMTStoreRefresh(pooledStore.RefreshTx)

			// LtHash runs alongside JMT.
			ltDigest := e_initLtHash(ctx, chainKv)
			ltCommit := commitment.NewLtHashCommitment(ltDigest)
			realBC.SetLtHashCommitment(ltCommit)

			jmtRC := commitment.NewJMTRootComputer(jmtCommit)
			ltAwareRC := commitment.NewLtHashAwareRootComputer(jmtRC, ltCommit)
			realBC.SetRootComputer(ltAwareRC)

			if cfg.NodeCfg.Chain == "private" {
				realBC.EnableJMTForBlockProcessing()
			}
			log.Info("State commitment: JMT-Blake3",
				"root", fmt.Sprintf("%x", jmtRoot[:8]),
				"version", jmtVersion,
			)

		case state.RootSchemeBMTBlake3:
			// --- BMT initialization (content-addressed Binary Merkle Tree) ---
			var bmtRoot bmt.Hash
			if rtx, err := chainKv.BeginRo(ctx); err == nil {
				bmtRoot, _ = bmtstore.ReadBMTRoot(rtx)
				rtx.Rollback()
			}
			// BMT uses content-addressed storage — MDBXStore tx is set per-block in writeBlockWithState.
			bmtNodeStore := bmtstore.NewMDBXStore(nil, bmtstore.BMTNodeTable)
			_ = bmtNodeStore // store reference kept via commitment
			var tree *bmt.Tree
			if bmtRoot == bmt.EmptyHash {
				tree = bmt.New(bmt.NewMemStore()) // in-memory until tx available
			} else {
				tree = bmt.NewFromRoot(bmt.NewMemStore(), bmtRoot)
			}
			bmtCommit := commitment.NewBMTCommitment(tree)
			realBC.SetBMTCommitment(bmtCommit)
			bmtRC := commitment.NewBMTRootComputer(bmtCommit)
			realBC.SetRootComputer(bmtRC)

			log.Info("State commitment: BMT-Blake3",
				"root", fmt.Sprintf("%x", bmtRoot[:8]),
			)

		case state.RootSchemeVerkle:
			// --- Verkle initialization (experimental, IPA/Banderwagon) ---
			// Read persisted version for crash recovery logging.
			var verkleVersion uint64
			if rtx, err := chainKv.BeginRo(ctx); err == nil {
				verkleVersion, _ = verklestore.ReadVerkleVersion(rtx)
				rtx.Rollback()
			}
			// In-memory tree; per-block FlushTo writes to MDBX in writeBlockWithState.
			verkleRoot := gverkle.New()
			verkleStore := libverkle.NewMemStore()
			verkleCommit := commitment.NewVerkleCommitment(verkleRoot, verkleStore)
			realBC.SetVerkleCommitment(verkleCommit)
			verkleRC := commitment.NewVerkleRootComputer(verkleCommit)
			realBC.SetRootComputer(verkleRC)

			log.Info("State commitment: Verkle (IPA/Banderwagon, experimental)",
				"persisted_block", verkleVersion,
			)

		case state.RootSchemeEthereumMPT:
			// --- MPT initialization (standard Ethereum HexPatriciaHashed) ---
			mptRC := commitment.NewPersistentMPTRootComputer()
			var mptRoot types.Hash
			var mptBlock uint64
			if rtx, err := chainKv.BeginRo(ctx); err == nil {
				mptRoot, err = commitment.ReadMPTRoot(rtx)
				if err != nil {
					log.Warn("Failed to read MPT root", "err", err)
				}
				// Restore HPH trie state from checkpoint (Erigon SeekCommitment pattern).
				if blockNum, trieState, err := commitment.ReadMPTTrieState(rtx); err == nil && len(trieState) > 0 {
					if err := mptRC.RestoreTrieState(trieState); err != nil {
						log.Warn("Failed to restore MPT trie state, will rebuild", "block", blockNum, "err", err)
					} else {
						mptBlock = blockNum
					}
				}
				rtx.Rollback()
			}
			realBC.SetMPTRootComputer(mptRC)
			realBC.SetRootComputer(mptRC)
			log.Info("State commitment: Ethereum-MPT (HexPatriciaHashed)",
				"root", fmt.Sprintf("%x", mptRoot[:8]),
				"checkpoint", mptBlock,
			)

		case state.RootSchemeQMDB:
			// QMDB live block production: reload the twig forest from the
			// persisted positional log (history-dependent root, so we replay the
			// exact slot layout, not a key-set rebuild), then drive it as the
			// block-processing root computer — writeBlockWithState flushes the
			// entry log per block. The in-RAM key->slot index is used because the
			// per-block evmRecord tx is read-only (cannot back the index with
			// MDBX on the live path). Also serve QMDB-native eth_getProof.
			// Wire the cold/leaf getters BEFORE LoadFrom: the rebuild faults
			// frozen leaves and cold entries through them. A failed reload can
			// leave a partially populated twig slice, so it is not an empty-tree
			// fallback: using it can panic in Root(), and an actually empty tree
			// would not reproduce this history-dependent root. Fail closed and
			// require repair/reseed instead -- after retrying on a fresh
			// computer and read view: rounds 35g-35j lost a node at every
			// fleet start to "twig metadata inconsistent" part way through the
			// scan, and the same store loaded whole on the next start ten
			// minutes later, so the first failure is a transient read, not
			// the disk.
			var qmdbRC *commitment.QMDBRootComputer
			var loadErr error
			// Round 35z2 (2026-09-08): all three attempts failed on three
			// different twigs (172644, 179292, 185698 of 219511) and the fourth
			// start, ten minutes later, loaded whole -- six attempts with a
			// longer pause ride out a longer burst of whatever the transient is.
			for attempt := 1; attempt <= 6; attempt++ {
				qmdbRC = commitment.NewQMDBRootComputer()
				rtx, err := chainKv.BeginRo(ctx)
				if err != nil {
					return nil, fmt.Errorf("open QMDB forest reload view: %w", err)
				}
				qmdbRC.SetCold(rtx)
				loadErr = qmdbRC.LoadFrom(rtx)
				rtx.Rollback()
				// rtx is dead from here; detach so nothing faults through it (the
				// first block's execution re-points at a live tx).
				qmdbRC.SetCold(nil)
				if loadErr == nil {
					break
				}
				log.Error("QMDB forest reload failed at startup", "attempt", attempt, "err", loadErr)
				time.Sleep(5 * time.Second)
			}
			if loadErr != nil {
				return nil, fmt.Errorf("reload QMDB forest: %w", loadErr)
			}
			qmdbRC.EnableUndoRecording()
			realBC.SetQMDBRootComputer(qmdbRC)
			realBC.SetStateProofProvider(internal.NewQMDBStateProofProvider(realBC.Config()))
			log.Info("State commitment: QMDB (twig forest, live block production)",
				"root", fmt.Sprintf("%x", qmdbRC.Root()))
			if commitment.QMDBStateReadMode() == commitment.QMDBReadOn {
				// N42_STATE_READ_QMDB=1: EVERY head-state account read -- the
				// miner's build, the txpool, RPC, the history fallback -- goes
				// to the tree, not only block execution. Round 26's first
				// attempt switched execution alone and the miner built on a
				// table the previous leg had frozen: state-root mismatch on
				// the first block, seven nodes wedged.
				modules.SetLatestAccountSource(commitment.NewQMDBLatestAccountSource(qmdbRC, chainKv))
				log.Info("head-state account reads served from the QMDB tree (N42_STATE_READ_QMDB=1): miner, txpool and RPC included")
			}
			if commitment.QMDBOnlyAccountWrites() {
				// N42_STATE_WRITE_QMDB_ONLY=1: stop maintaining the plain
				// `Account` table. Only sound once reads no longer touch it.
				if !modules.LatestAccountSourceInstalled() {
					return nil, fmt.Errorf("N42_STATE_WRITE_QMDB_ONLY=1 requires N42_STATE_READ_QMDB=1: the plain Account table stops being written, so reads must not depend on it")
				}
				modules.SetPlainAccountWriteSkipped(true)
				// Record the head at the FIRST enablement so a repair can later
				// rebuild exactly the rows the table missed: every address in an
				// AccountChangeSet from this block on, re-read from the tree.
				var frozenAt uint64
				if err := chainKv.Update(ctx, func(tx kv.RwTx) error {
					if v, err := tx.GetOne(modules.QMDBMeta, commitment.QMDBAccountFrozenAtKey); err == nil && len(v) == 8 {
						frozenAt = binary.BigEndian.Uint64(v)
						return nil
					}
					frozenAt = bc.CurrentBlock().Number64().Uint64() + 1
					var buf [8]byte
					binary.BigEndian.PutUint64(buf[:], frozenAt)
					return tx.Put(modules.QMDBMeta, commitment.QMDBAccountFrozenAtKey, buf[:])
				}); err != nil {
					return nil, fmt.Errorf("record the Account table freeze: %w", err)
				}
				log.Warn("QMDB-only account persistence: the plain Account table is frozen; snap-sync serving and state dumps read a stale table (measurement lever, docs/QS_BLOCK_TIME_BUDGET.md round 26)",
					"frozenAt", frozenAt)
			}

		case state.RootSchemeLegacyKeccak:
			log.Info("State commitment: Legacy-Keccak (no tree)")

		default:
			return nil, fmt.Errorf("unsupported state scheme: %s", stateScheme)
		}
	}
	if commitment.QMDBOnlyAccountWrites() && !modules.PlainAccountWriteSkipped() {
		return nil, fmt.Errorf("N42_STATE_WRITE_QMDB_ONLY=1 needs a chain committing with QMDB (state scheme %s does not)", stateScheme)
	}

	// Initialize ZK proving if configured.
	var zkProverSvc *zkprover.Service
	var zkVerify *zkverifier.Verifier
	if cfg.NodeCfg.ZKProving || cfg.ZKProverCfg.Enabled {
		if realBC, ok := bc.(*internal.BlockChain); ok {
			realBC.SetZKProving(true, cfg.ZKProverCfg.RequireProof)
		}
		zkVerify = zkverifier.NewVerifier()
		if cfg.ZKProverCfg.RequireProof && !zkVerify.CryptographicReady() {
			return nil, fmt.Errorf("zkprover: require_proof cannot be enabled before cryptographic verification is ready")
		}
		if cfg.ZKProverCfg.Enabled && !cfg.ZKProverCfg.VerifyOnly {
			var err error
			zkProverSvc, err = zkprover.NewService(&cfg.ZKProverCfg)
			if err != nil {
				log.Warn("Failed to create ZK prover service", "err", err)
			} else {
				// Inject SP1 prover client.
				sp1Client := zkprover.NewSP1ProverClient(
					cfg.ZKProverCfg.ProverAddr,
					cfg.ZKProverCfg.GuestBinary,
					cfg.ZKProverCfg.SP1CLIPath,
				)
				zkProverSvc.SetProverClient(sp1Client)
				log.Info("ZK prover service configured",
					"addr", cfg.ZKProverCfg.ProverAddr,
					"guest", cfg.ZKProverCfg.GuestBinary,
					"simulate", cfg.ZKProverCfg.ProverAddr == "",
				)
			}
		}
	}

	// Initialize snapshot acceleration tree if configured.
	if cfg.SnapshotAccelCfg.Enable {
		if realBC, ok := bc.(*internal.BlockChain); ok {
			cache := layered.ExtractCache(chainKv)
			if cache != nil {
				cur := realBC.CurrentBlock()
				if cur == nil {
					return nil, fmt.Errorf("snapshot acceleration requires current block")
				}
				curBlockNumber := blockNumberOrZero(cur)
				tree := statesnapshot.NewTree(cache, curBlockNumber, cur.Hash(), cfg.SnapshotAccelCfg.MaxDiffLayers)

				// Enable persistence if configured.
				if cfg.SnapshotAccelCfg.Persist {
					tree.SetDB(chainKv)

					// Check if flat snapshot generation is complete.
					if tx, err := chainKv.BeginRo(ctx); err == nil {
						complete, _ := rawdb.IsSnapshotGenComplete(tx)
						tx.Rollback()
						if complete {
							tree.SetGenReady(true)
							log.Info("Snapshot flat tables ready for reads")
						} else {
							// Start background generation.
							gen := statesnapshot.NewGenerator(chainKv, cur.Hash(), curBlockNumber)
							go func() {
								gen.Run(ctx)
								// Only mark ready if generation actually completed
								// (not cancelled or failed mid-way).
								if ctx.Err() == nil {
									if rtx, err := chainKv.BeginRo(context.Background()); err == nil {
										complete, _ := rawdb.IsSnapshotGenComplete(rtx)
										rtx.Rollback()
										if complete {
											tree.SetGenReady(true)
										}
									}
								}
							}()
						}
					}

					// Load journal for crash recovery.
					if tx, err := chainKv.BeginRo(ctx); err == nil {
						if err := statesnapshot.LoadJournal(tx, tree); err != nil {
							log.Warn("Failed to load snapshot journal", "err", err)
						}
						tx.Rollback()
					}
				}

				realBC.SetSnapshotTree(tree)
			}
		}
	}

	depositContract, err = resolveDepositContract(ctx, bc, chainKv, cfg.ChainCfg)
	if err != nil {
		return nil, err
	}

	// The pool used to read a package-level default that nothing could reach,
	// so its capacity was fixed at roughly 6,000 transactions regardless of
	// configuration -- smaller than one block above a ~126M gas ceiling, and
	// therefore an invisible ceiling on throughput.
	poolCfg := cfg.TxPoolCfg
	if fixed := poolCfg.Sanitize(); len(fixed) > 0 {
		log.Warn("Transaction pool config had unusable values, defaults applied", "fields", fixed)
	}
	pool, err := txspool.NewTxsPoolWithConfig(ctx, bc, depositContract, txspool.FromConf(poolCfg))
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction pool: %w", err)
	}
	log.Info("Transaction pool configured",
		"globalSlots", poolCfg.GlobalSlots, "globalQueue", poolCfg.GlobalQueue,
		"accountSlots", poolCfg.AccountSlots, "accountQueue", poolCfg.AccountQueue,
		"lifetime", poolCfg.Lifetime)

	// Block import reuses the pool's already-recovered senders instead of
	// re-deriving every signature (see SenderHintSource).
	if realBC, ok := bc.(*internal.BlockChain); ok {
		realBC.SetSenderHintSource(pool)
	}

	is := initialsync.NewService(ctx, &initialsync.Config{
		Chain: bc,
		P2P:   p2p,
	})

	snapSyncSvc := snapsync.NewService(ctx, &snapsync.Config{
		P2P:      p2p,
		Chain:    bc,
		DB:       chainKv,
		SnapSync: &cfg.SnapSyncCfg,
	})

	checkpointSvc := checkpoint.NewService(&checkpoint.Config{
		Checkpoint: &cfg.CheckpointCfg,
		P2P:        p2p,
		Chain:      bc,
		DB:         chainKv,
	})

	// Peers older than the RLP block wire format send protobuf. Reconstructing
	// from protobuf drops any header field types_pb has no slot for, which
	// changes the block hash, so it is only allowed on chains that never carry
	// one. See sync.AllowLegacyProtoBlocks.
	n42sync.AllowLegacyProtoBlocks(chainConfig)

	syncOpts := []n42sync.Option{
		n42sync.WithP2P(p2p),
		n42sync.WithChainService(bc),
		n42sync.WithInitialSync(is),
	}
	// Keep the sync/status-handshake genesis hash consistent with the P2P fork
	// digest (p2pGenesisHash already folds in the chain constant and any
	// --p2p.genesis-override). Advertising two different hashes would cause peers
	// to reject us with a wrong-network goodbye.
	syncOpts = append(syncOpts, n42sync.WithOverrideGenesisHash(p2pGenesisHash))
	if cfg.P2PCfg.TxGossipEnabled && pool != nil {
		syncOpts = append(syncOpts, n42sync.WithTxPool(pool))
	}
	syncServer, err := n42sync.NewService(ctx, syncOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create sync service: %w", err)
	}

	miner := miner.NewMiner(ctx, cfg, bc, engine, pool, nil)

	// Wire ZK prover service to the miner so it can submit proof requests
	// after producing blocks.
	if zkProverSvc != nil {
		miner.SetZKProverService(zkProverSvc)
	}

	keyDir, isEphem, err := getKeyStoreDir(&cfg.NodeCfg)
	if err != nil {
		return nil, err
	}
	// Creates an empty AccountManager with no backends. Callers (e.g. cmd/n42)
	// are required to add the backends later on.
	accman := accounts.NewManager(&accounts.Config{InsecureUnlockAllowed: cfg.NodeCfg.InsecureUnlockAllowed})

	var engineStateAdapter *api.EngineStateAdapter
	if profile.IsEthereumEL() {
		var adapterFreezer *freezer.Freezer
		if realBC, ok := bc.(*internal.BlockChain); ok {
			if f, ok := realBC.Freezer().(*freezer.Freezer); ok {
				adapterFreezer = f
			}
		}
		engineStateAdapter = api.NewEngineStateAdapter(chainKv, adapterFreezer, cfg.ChainCfg, engine)

		// Wire CS warm tier if configured (--cs-warm-dir).
		// On error opening warm, log a warning and continue with
		// live-freezer-only — Reorg still works, just without the
		// disk-savings benefit. Failing the node startup over a
		// missing/corrupt warm tier would be over-strict; warm is
		// an optional optimization, the live freezer is canonical.
		if dir := cfg.NodeCfg.CSWarmDir; dir != "" {
			warm, err := cs.Open(dir)
			if err != nil {
				log.Warn("CS warm tier disabled: failed to open warm dir",
					"dir", dir, "err", err)
			} else {
				var src cs.Source = warm
				if adapterFreezer != nil {
					// Tiered: warm first (hot path), full freezer as fallback.
					src = cs.NewTieredSource(warm, cs.NewFreezerSource(adapterFreezer))
				}
				engineStateAdapter.WithCSSource(src)
				m := warm.Meta()
				log.Info("CS warm tier enabled",
					"dir", dir, "base_block", m.BaseBlock,
					"head_block", m.HeadBlock, "keep_blocks", m.KeepBlocks)
			}
		}
	}

	node = Node{
		cliCtx:          cliCtx,
		ctx:             ctx,
		cancel:          cancel,
		config:          cfg,
		profile:         profile,
		runtimePlan:     resolveAuxiliaryRuntimePlan(cfg, profile),
		miner:           miner,
		genesisBlock:    genesisBlock,
		blockChain:      bc,
		txIndexer:       nodeTxIndexer,
		eraStore:        nodeEraStore,
		db:              chainKv,
		frameDB:         frameKv,
		shutDown:        make(chan struct{}),
		txspool:         pool,
		engine:          engine,
		depositContract: depositContract,

		exexManager:     exexMgr,
		zkProverService: zkProverSvc,
		zkVerifier:      zkVerify,
		inprocHandler:   jsonrpc.NewServer(),
		http:            newHTTPServer(),
		ws:              newHTTPServer(),
		wsAuth:          newHTTPServer(),
		httpAuth:        newHTTPServer(),
		ipc:             newIPCServer(&cfg.NodeCfg),
		etherbase:       types.HexToAddress(cfg.Miner.Etherbase),

		accman:             accman,
		keyDir:             keyDir,
		keyDirTemp:         isEphem,
		engineStateAdapter: engineStateAdapter,

		p2pGenesisHash: p2pGenesisHash,
		p2p:            p2p,
		sync:           syncServer,
		is:             is,
		checkpointSync: checkpointSvc,
		snapSync:       snapSyncSvc,
	}

	if err = setAccountManagerBackends(&node, &cfg.NodeCfg); err != nil {
		log.Errorf("Failed to set account manager backends: %v", err)
	}

	gpoParams := cfg.GPO
	if gpoParams.Default == nil {
		gpoParams.Default = cfg.Miner.GasPrice
	}

	// Print beautiful startup banner
	actualGenesisHash := genesisBlock.Hash()
	expectedGenesisHash, hasExpectedGenesis := configuredGenesis.canonicalHash()

	// Get chain info from description
	chainName := cfg.NodeCfg.Chain
	if chainName == "" {
		chainName = "mainnet"
	}
	consensusName := cfg.ChainCfg.Consensus.DisplayName()

	// Print the pretty banner with system info
	currentBlockNumber := uint64(0)
	if currentBlock := bc.CurrentBlock(); currentBlock != nil {
		currentBlockNumber = blockNumberOrZero(currentBlock)
	}
	log.PrintBanner(
		params.VersionWithMeta,
		fmt.Sprintf("%s (ID: %s)", chainName, cfg.ChainCfg.ChainID.String()),
		consensusName,
		actualGenesisHash.String(),
		currentBlockNumber,
		fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		runtime.Version(),
		runtime.NumCPU(),
		cfg.NodeCfg.DataDir,
	)

	// Check genesis hash mismatch
	if hasExpectedGenesis && actualGenesisHash != expectedGenesisHash {
		log.PrintErrorBox("Genesis Hash Mismatch", []string{
			fmt.Sprintf("Expected: %s", expectedGenesisHash.String()[:20]+"..."),
			fmt.Sprintf("Actual:   %s", actualGenesisHash.String()[:20]+"..."),
		})
	}

	// Initialize OpenTelemetry tracing if configured.
	tracingShutdown, err := tracing.Init(tracing.Config{
		Enable:     cfg.TracingCfg.Enable,
		Endpoint:   cfg.TracingCfg.Endpoint,
		SampleRate: cfg.TracingCfg.SampleRate,
	})
	if err != nil {
		log.Warn("Failed to initialize tracing, continuing without it", "err", err)
		tracingShutdown = func(context.Context) error { return nil }
	}
	node.tracingShutdown = tracingShutdown

	// Stateless validation mode: validate blocks via Merkle proof witnesses only.
	if cfg.NodeCfg.StatelessEnabled {
		node.statelessMode = true
		node.statelessValidator = stateless.NewValidator(cfg.ChainCfg)
		log.Info("Stateless validation mode enabled — blocks will be verified via JMT witnesses",
			"codeCacheSize", 4096)
	}

	node.api = api.NewAPI(bc, chainKv, engine, pool, node.AccountManager(), cfg.ChainCfg)
	node.api.SetRPCMaxGasPrice(cfg.NodeCfg.RPCMaxGasPrice)
	node.api.SetGpo(api.NewOracle(bc, miner, cfg.ChainCfg, gpoParams))
	node.api.SetP2P(&p2pAdminAdapter{svc: p2p, node: &node})
	node.api.SetMiner(&minerAdminAdapter{m: miner})

	// Optional cold tx-hash → block index (txlookup RecSplit segments). Lets
	// eth_getTransactionByHash resolve historical txs that have aged out of the
	// MDBX TxLookup table. Opt-in via N42_TXINDEX_DIR (built by cmd/txindex-rebuild);
	// off by default so default nodes are unchanged.
	if dir := os.Getenv("N42_TXINDEX_DIR"); dir != "" {
		if svc, err := txlookup.NewService(dir); err != nil {
			log.Warn("txindex cold lookup: open failed", "dir", dir, "err", err)
		} else {
			node.api.SetTxIndexService(svc)
			log.Info("txindex cold tx-hash lookup enabled", "dir", dir, "segments", svc.SegmentCount())
		}
	}
	// Optional cold blockHash → number index (blockhashindex RecSplit segments).
	// Lets getBlockByHash & friends resolve historical block hashes that have aged
	// out of the MDBX HeaderNumber table. Opt-in via N42_BLOCKHASH_DIR (built by
	// cmd/blockhash-rebuild); off by default.
	if dir := os.Getenv("N42_BLOCKHASH_DIR"); dir != "" {
		if svc, err := blockhashindex.NewService(dir); err != nil {
			log.Warn("blockhash cold lookup: open failed", "dir", dir, "err", err)
		} else {
			node.api.SetBlockHashIndex(svc)
			log.Info("blockhash cold lookup enabled", "dir", dir, "segments", svc.SegmentCount())
		}
	}

	// Create ERC-4337 bundler service if enabled.
	if cfg.BundlerCfg.Enabled {
		bundleInterval := time.Duration(cfg.BundlerCfg.BundleIntervalSec) * time.Second
		if bundleInterval <= 0 {
			bundleInterval = 12 * time.Second // default
		}
		bundlerCfg := &bundler.Config{
			Enabled:        true,
			MaxPoolSize:    cfg.BundlerCfg.MaxPoolSize,
			MaxBundleSize:  cfg.BundlerCfg.MaxBundleSize,
			BundleInterval: bundleInterval,
		}
		chainID, err := bundlerChainID(cfg.ChainCfg)
		if err != nil {
			return nil, err
		}
		node.bundlerService = bundler.NewBundlerService(bundlerCfg, chainID)
	}

	// Create PeerDAS service if enabled.
	if cfg.PeerDASCfg.IsEnabled() {
		peerID := node.p2p.PeerID()
		nodeIDBytes := []byte(peerID)
		peerdasCfg := peerdas.Config{
			Enable:          true,
			CustodyCount:    cfg.PeerDASCfg.CustodyCount,
			SamplingEnabled: cfg.PeerDASCfg.SamplingEnabled,
			SampleCount:     cfg.PeerDASCfg.SampleCount,
		}
		node.peerdasService = peerdas.NewService(peerdasCfg, nodeIDBytes, node.db)
		node.peerdasService.SetBlockProvider(&peerdasBlockProvider{node: &node})
	}

	success = true
	chainKv = nil // prevent deferred cleanup from closing the DB now owned by node
	return &node, nil
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
