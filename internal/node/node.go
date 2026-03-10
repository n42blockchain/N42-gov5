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

package node

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash/crc32"
	"math/big"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/c2h5oh/datasize"
	"github.com/gofrs/flock"
	"github.com/holiman/uint256"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	pkgerrors "github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"go.uber.org/zap"
	"golang.org/x/sync/semaphore"

	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/accounts/keystore"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/hexutil"
	prometheus "github.com/n42blockchain/N42/common/metrics"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/contracts/deposit"
	n42deposit "github.com/n42blockchain/N42/contracts/deposit/amt"
	fujideposit "github.com/n42blockchain/N42/contracts/deposit/fuji"
	nftdeposit "github.com/n42blockchain/N42/contracts/deposit/nft"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/apoa"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/internal/debug"
	"github.com/n42blockchain/N42/internal/exex"
	"github.com/n42blockchain/N42/internal/exex/extensions"
	"github.com/n42blockchain/N42/internal/miner"
	"github.com/n42blockchain/N42/internal/p2p"
	n42sync "github.com/n42blockchain/N42/internal/sync"
	"github.com/n42blockchain/N42/internal/sync/checkpoint"
	initialsync "github.com/n42blockchain/N42/internal/sync/initialsync"
	"github.com/n42blockchain/N42/internal/snapshot"
	"github.com/n42blockchain/N42/internal/sync/snapsync"
	"github.com/n42blockchain/N42/internal/tracers"
	"github.com/n42blockchain/N42/internal/tracing"
	"github.com/n42blockchain/N42/internal/txgen"
	"github.com/n42blockchain/N42/internal/txspool"
	"github.com/n42blockchain/N42/lib/common/cmp"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	nodeMetrics "github.com/n42blockchain/N42/internal/metrics"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	statesnapshot "github.com/n42blockchain/N42/modules/state/snapshot"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
	"github.com/n42blockchain/N42/utils"
)

const datadirJWTKey = "jwtsecret" // Path within the datadir to the node's jwt secret

type Node struct {
	cliCtx       *cli.Context
	ctx          context.Context
	cancel       context.CancelFunc
	config       *conf.Config
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
	sync            *n42sync.Service
	is              *initialsync.Service
	checkpointSync  *checkpoint.Service
	snapSync        *snapsync.Service
	accman          *accounts.Manager

	api     *api.API
	rpcAPIs []jsonrpc.API

	http          *httpServer
	ipc           *ipcServer
	ws            *httpServer
	httpAuth      *httpServer
	wsAuth        *httpServer
	inprocHandler *jsonrpc.Server
	rateLimiter   *jsonrpc.RateLimiter
	pruner          *Pruner
	historyExpirer  *HistoryExpirer
	snapshotMgr     *snapshot.Manager

	exexManager *exex.Manager // Execution Extensions manager

	keyDir     string // key store directory
	keyDirTemp bool   // If true, key directory will be removed by Stop

	tracingShutdown func(context.Context) error // flushes and stops the OTel tracer provider

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
	defer func() {
		if !success {
			cancel()
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
		chainKv         kv.RwDB
		err             error
	)

	chainKv, err = OpenDatabase(ctx, cfg, nil, kv.ChainDB.String())
	if err != nil {
		return nil, err
	}

	if err := chainKv.View(ctx, func(tx kv.Tx) error {
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

	if genesisHash == (types.Hash{}) {
		if cfg.NodeCfg.Chain == "private" {
			// Private/dev chain: use a built-in devnet genesis so that
			// --dev works out of the box without a separate config file.
			genesisConfig = devnetGenesisBlock(cfg)
			chainConfig = genesisConfig.Config
		} else {
			hashPtr := params.GenesisHashByChainName(cfg.NodeCfg.Chain)
			if hashPtr == nil {
				return nil, fmt.Errorf("unknown chain: %s", cfg.NodeCfg.Chain)
			}
			genesisHash = *hashPtr
			genesisConfig = internal.GenesisByChainName(cfg.NodeCfg.Chain)
			chainConfig = params.ChainConfigByChainName(cfg.NodeCfg.Chain)
		}
		if err := chainKv.Update(ctx, func(tx kv.RwTx) error {
			var writeErr error
			genesisBlock, writeErr = WriteGenesisBlock(tx, genesisConfig)
			if writeErr != nil {
				return writeErr
			}
			if cfg.NodeCfg.Chain == "private" {
				genesisHash = genesisBlock.Hash()
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}

	// Update ChainConfig on every startup for non-private chains.
	if cfg.NodeCfg.Chain != "private" {
		if err := chainKv.Update(ctx, func(tx kv.RwTx) error {
			genesisHash = *params.GenesisHashByChainName(cfg.NodeCfg.Chain)
			genesisConfig = internal.GenesisByChainName(cfg.NodeCfg.Chain)
			return WriteChainConfig(tx, genesisHash, genesisConfig)
		}); err != nil {
			return nil, err
		}
	}

	// Acquire the instance directory lock.
	if err := node.openDataDir(cfg); err != nil {
		return nil, err
	}

	cfg.ChainCfg = chainConfig

	p2p, err := p2p.NewService(ctx, genesisBlock.Hash(), cfg.P2PCfg, cfg.NodeCfg)
	if err != nil {
		return nil, err
	}

	switch cfg.ChainCfg.Consensus {
	case params.CliqueConsensus:
		engine = apoa.New(cfg.ChainCfg.Clique, chainKv)
	case params.AposConsensu:
		engine = apos.New(cfg.ChainCfg.Apos, chainKv, cfg.ChainCfg)
	case params.Faker:
		engine = apos.NewFaker()
	default:
		return nil, fmt.Errorf("invalid engine name %s", cfg.ChainCfg.Consensus)
	}

	bc, err := internal.NewBlockChain(ctx, genesisBlock, engine, chainKv, p2p, cfg.ChainCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create blockchain: %w", err)
	}

	// Enable parallel EVM execution, state prefetching, and ancient DB if configured.
	if realBC, ok := bc.(*internal.BlockChain); ok {
		if cfg.NodeCfg.ParallelEVM {
			realBC.SetParallelEVM(true)
		}
		if cfg.NodeCfg.Prefetch {
			realBC.SetPrefetch(true)
		}
		if cfg.NodeCfg.AncientDB {
			ancientPath := filepath.Join(cfg.NodeCfg.DataDir, "ancient")
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
					return cur.Number64().Uint64()
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

	// Initialize snapshot acceleration tree if configured.
	if cfg.SnapshotAccelCfg.Enable {
		if realBC, ok := bc.(*internal.BlockChain); ok {
			cache := layered.ExtractCache(chainKv)
			if cache != nil {
				cur := realBC.CurrentBlock()
				tree := statesnapshot.NewTree(cache, cur.Number64().Uint64(), cur.Hash(), cfg.SnapshotAccelCfg.MaxDiffLayers)
				realBC.SetSnapshotTree(tree)
			}
		}
	}

	if cfg.ChainCfg.Apos != nil {
		depositContracts := make(map[types.Address]deposit.DepositContract)
		entries := []struct {
			addr     string
			name     string
			contract deposit.DepositContract
		}{
			{cfg.ChainCfg.Apos.DepositContract, "DepositContract", new(n42deposit.Contract)},
			{cfg.ChainCfg.Apos.DepositNFTContract, "DepositNFTContract", new(nftdeposit.Contract)},
			{cfg.ChainCfg.Apos.DepositFUJIContract, "DepositFUJIContract", new(fujideposit.Contract)},
		}
		for _, e := range entries {
			if e.addr == "" {
				continue
			}
			var addr types.Address
			if !addr.DecodeString(e.addr) {
				return nil, fmt.Errorf("cannot decode %s address: %s", e.name, e.addr)
			}
			depositContracts[addr] = e.contract
		}
		depositContract = deposit.NewDeposit(ctx, bc, chainKv, depositContracts)
	}

	pool, err := txspool.NewTxsPool(ctx, bc, depositContract)
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction pool: %w", err)
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

	syncServer, err := n42sync.NewService(
		ctx,
		n42sync.WithP2P(p2p),
		n42sync.WithChainService(bc),
		n42sync.WithInitialSync(is),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create sync service: %w", err)
	}

	miner := miner.NewMiner(ctx, cfg, bc, engine, pool, nil)

	keyDir, isEphem, err := getKeyStoreDir(&cfg.NodeCfg)
	if err != nil {
		return nil, err
	}
	// Creates an empty AccountManager with no backends. Callers (e.g. cmd/n42)
	// are required to add the backends later on.
	accman := accounts.NewManager(&accounts.Config{InsecureUnlockAllowed: cfg.NodeCfg.InsecureUnlockAllowed})

	node = Node{
		cliCtx:          cliCtx,
		ctx:             ctx,
		cancel:          cancel,
		config:          cfg,
		miner:           miner,
		genesisBlock:    genesisBlock,
		blockChain:      bc,
		db:              chainKv,
		shutDown:        make(chan struct{}),
		txspool:         pool,
		engine:          engine,
		depositContract: depositContract,

		exexManager:   exexMgr,
		inprocHandler: jsonrpc.NewServer(),
		http:          newHTTPServer(),
		ws:            newHTTPServer(),
		wsAuth:        newHTTPServer(),
		httpAuth:      newHTTPServer(),
		ipc:           newIPCServer(&cfg.NodeCfg),
		etherbase:     types.HexToAddress(cfg.Miner.Etherbase),

		accman:     accman,
		keyDir:     keyDir,
		keyDirTemp: isEphem,

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
	expectedGenesisHash := params.MainnetGenesisHash
	if cfg.NodeCfg.Chain == "testnet" {
		expectedGenesisHash = params.TestnetGenesisHash
	}

	// Get chain info from description
	chainName := cfg.NodeCfg.Chain
	if chainName == "" {
		chainName = "mainnet"
	}
	consensusName := string(cfg.ChainCfg.Consensus)
	if cfg.ChainCfg.Consensus == params.AposConsensu {
		consensusName = "Mobile Consensus"
	}

	// Print the pretty banner with system info
	log.PrintBanner(
		params.VersionWithMeta,
		fmt.Sprintf("%s (ID: %s)", chainName, cfg.ChainCfg.ChainID.String()),
		consensusName,
		actualGenesisHash.String(),
		bc.CurrentBlock().Number64().Uint64(),
		fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		runtime.Version(),
		runtime.NumCPU(),
		cfg.NodeCfg.DataDir,
	)

	// Check genesis hash mismatch
	if actualGenesisHash != expectedGenesisHash {
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

	node.api = api.NewAPI(bc, chainKv, engine, pool, node.AccountManager(), cfg.ChainCfg)
	node.api.SetGpo(api.NewOracle(bc, miner, cfg.ChainCfg, gpoParams))
	node.api.SetP2P(&p2pAdminAdapter{svc: p2p})
	node.api.SetMiner(&minerAdminAdapter{m: miner})
	success = true
	return &node, nil
}

func (n *Node) Start() error {
	n.startStopLock.Lock()
	defer n.startStopLock.Unlock()

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

	log.PrintStartupProgress(1, 6, "Blockchain")
	if err := n.blockChain.Start(); err != nil {
		log.Errorf("failed setup blockChain service, err: %v", err)
		return err
	}

	// Start ExEx manager after blockchain is running.
	if n.exexManager != nil {
		n.exexManager.Start(n.ctx)
	}

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

		// Authorize the consensus engine with the miner's signing function.
		if poa, ok := n.engine.(*apoa.Apoa); ok {
			wallet, findErr := n.accman.Find(accounts.Account{Address: eb})
			if wallet == nil || findErr != nil {
				log.Error("Etherbase account unavailable locally", "err", findErr)
				return fmt.Errorf("signer missing: %v", findErr)
			}
			poa.Authorize(eb, wallet.SignData)
		} else if pos, ok := n.engine.(*apos.APos); ok {
			wallet, findErr := n.accman.Find(accounts.Account{Address: eb})
			if wallet == nil || findErr != nil {
				log.Error("Etherbase account unavailable locally", "err", findErr)
				return fmt.Errorf("signer missing: %v", findErr)
			}
			pos.Authorize(eb, wallet.SignData)
		}

		n.miner.SetCoinbase(eb)
		n.miner.Start()
	}

	if pos, ok := n.engine.(*apos.APos); ok {
		pos.SetBlockChain(n.blockChain)
	}

	log.PrintStartupProgress(2, 6, "JSON-RPC services")

	n.rpcAPIs = append(n.rpcAPIs, n.engine.APIs(n.blockChain)...)
	n.rpcAPIs = append(n.rpcAPIs, n.api.Apis()...)
	n.rpcAPIs = append(n.rpcAPIs, tracers.APIs(n.api)...)
	n.rpcAPIs = append(n.rpcAPIs, debug.APIs()...)

	if err := n.startRPC(); err != nil {
		log.Error("failed start jsonrpc service", zap.Error(err))
		return err
	}

	log.PrintStartupProgress(3, 6, "P2P networking")
	n.p2p.Start()

	log.PrintStartupProgress(4, 6, "Sync service")
	n.sync.Start()

	log.PrintStartupProgress(5, 6, "Metrics")
	n.SetupMetrics(n.config.MetricsCfg)

	log.PrintStartupProgress(6, 6, "Deposit contract")
	if n.depositContract != nil {
		n.depositContract.Start()
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

	// Start snapshot manager if enabled
	if n.config.SnapshotCfg.Enable {
		bp := &nodeBlockProvider{node: n}
		n.snapshotMgr = snapshot.NewManager(n.config.SnapshotCfg, n.db, bp)
		n.snapshotMgr.Start()
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
		n.sync.SetEarliestBlock(n.historyExpirer.EarliestBlock)
	}

	// Start transaction generator if enabled
	if n.config.DevCfg.TxGenEnabled {
		n.startTxGenerator()
	}

	log.PrintSuccess("All services started")

	return nil
}

// startTxGenerator initializes and starts the transaction generator for development testing.
func (n *Node) startTxGenerator() {
	txgenConfig := &txgen.Config{
		Enabled:        n.config.DevCfg.TxGenEnabled,
		MaxTxsPerBlock: n.config.DevCfg.TxGenMaxPerBlock,
		Interval:       n.config.DevCfg.TxGenInterval,
		GasPrice:       uint64(n.config.DevCfg.TxGenGasPrice),
		GasLimit:       21000,
		Value:          1000,
		FaucetAmount:   1000000000000000000, // 1 ETH per test account
	}

	chainID, _ := uint256.FromBig(n.config.ChainCfg.ChainID)

	// Use etherbase (coinbase) as the faucet source
	coinbase := n.etherbase

	n.txGenerator = txgen.New(n.ctx, txgenConfig, n.txspool, chainID, coinbase, n.accman)

	n.txGenerator.FundAccounts()
	n.txGenerator.Start()
}

// getAPIs return two sets of APIs, both the ones that do not require
// authentication, and the complete set
func (n *Node) getAPIs() (unauthenticated, all []jsonrpc.API) {
	for _, api := range n.rpcAPIs {
		if !api.Authenticated {
			unauthenticated = append(unauthenticated, api)
		}
	}
	return unauthenticated, n.rpcAPIs
}

func (n *Node) startInProc() error {
	for _, api := range n.rpcAPIs {
		if err := n.inprocHandler.RegisterName(api.Namespace, api.Service); err != nil {
			return err
		}
	}
	return nil
}

func (n *Node) stopInProc() {
	n.inprocHandler.Stop()
}

func (n *Node) openDataDir(cfg *conf.Config) error {
	if cfg.NodeCfg.DataDir == "" {
		return nil // ephemeral
	}

	if err := os.MkdirAll(cfg.NodeCfg.DataDir, 0700); err != nil {
		return err
	}
	// Lock the instance directory to prevent concurrent use by another instance as well as
	// accidental use of the instance directory as a database.
	n.dirLock = flock.New(filepath.Join(cfg.NodeCfg.DataDir, "LOCK"))

	if locked, err := n.dirLock.TryLock(); err != nil {
		return err
	} else if !locked {
		return ErrDatadirUsed
	}
	return nil
}

func (n *Node) closeDataDir() {
	// Release instance directory lock.
	if n.dirLock != nil && n.dirLock.Locked() {
		n.dirLock.Unlock()
		n.dirLock = nil
	}
}

// obtainJWTSecret loads the jwt-secret, either from the provided config,
// or from the default location. If neither of those are present, it generates
// a new secret and stores to the default location.
func (n *Node) obtainJWTSecret(cliParam string) ([]byte, error) {
	fileName := cliParam
	if len(fileName) == 0 {
		// no path provided, use default
		fileName = path.Join(n.config.NodeCfg.DataDir, datadirJWTKey)
	}
	// try reading from file
	if data, err := os.ReadFile(fileName); err == nil {
		jwtSecret, err := hexutil.Decode(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, pkgerrors.Wrap(err, fmt.Sprintf("failed to decode hex (%s) string", strings.TrimSpace(string(data))))
		}
		if len(jwtSecret) == 32 {
			log.Info("Loaded JWT secret file", "path", fileName, "crc32", fmt.Sprintf("%#x", crc32.ChecksumIEEE(jwtSecret)))
			return jwtSecret, nil
		}
		log.Error("Invalid JWT secret", "path", fileName, "length", len(jwtSecret))
		return nil, errors.New("invalid JWT secret")
	}
	// Need to generate one
	jwtSecret := make([]byte, 32)
	if _, err := rand.Read(jwtSecret); err != nil {
		return nil, fmt.Errorf("failed to generate JWT secret: %w", err)
	}

	if err := os.WriteFile(fileName, []byte(hexutil.Encode(jwtSecret)), 0600); err != nil {
		return nil, err
	}
	log.Info("Generated JWT secret", "path", fileName)
	return jwtSecret, nil
}

func (n *Node) startRPC() error {
	openAPIs, allAPIs := n.getAPIs()

	if err := n.startInProc(); err != nil {
		return err
	}

	// Create rate limiter if configured
	var rl *jsonrpc.RateLimiter
	if n.config.NodeCfg.HTTPRateLimit > 0 {
		burst := n.config.NodeCfg.HTTPRateLimitBurst
		if burst <= 0 {
			burst = n.config.NodeCfg.HTTPRateLimit * 2
		}
		cfg := jsonrpc.DefaultRateLimitConfig()
		cfg.RequestsPerSecond = n.config.NodeCfg.HTTPRateLimit
		cfg.BurstSize = burst
		rl = jsonrpc.NewRateLimiter(cfg)
		n.rateLimiter = rl
		log.Info("RPC rate limiting enabled", "rps", n.config.NodeCfg.HTTPRateLimit, "burst", burst)
	}

	// Set health provider on all HTTP servers
	hp := &nodeHealthProvider{node: n}
	n.http.healthProvider = hp
	n.httpAuth.healthProvider = hp

	if n.config.NodeCfg.HTTP {
		config := httpConfig{
			CorsAllowedOrigins: utils.SplitAndTrim(n.config.NodeCfg.HTTPCors),
			Vhosts:             []string{"*"},
			Modules:            utils.SplitAndTrim(n.config.NodeCfg.HTTPApi),
			prefix:             "",
			rateLimiter:        rl,
		}
		port, _ := strconv.Atoi(n.config.NodeCfg.HTTPPort)
		if err := n.http.setListenAddr(n.config.NodeCfg.HTTPHost, port); err != nil {
			return err
		}
		if err := n.http.enableRPC(n.rpcAPIs, config); err != nil {
			return err
		}
		if err := n.http.start(); err != nil {
			return err
		}
	}

	// Configure WebSocket.
	if n.config.NodeCfg.WS {
		port, _ := strconv.Atoi(n.config.NodeCfg.WSPort)
		if err := n.ws.setListenAddr(n.config.NodeCfg.WSHost, port); err != nil {
			return err
		}
		config := wsConfig{
			Modules:   utils.SplitAndTrim(n.config.NodeCfg.WSApi),
			Origins:   utils.SplitAndTrim(n.config.NodeCfg.WSOrigins),
			prefix:    "",
			jwtSecret: []byte{},
		}
		if err := n.ws.enableWS(n.rpcAPIs, config); err != nil {
			return err
		}
		if err := n.ws.start(); err != nil {
			return err
		}
	}

	// Configure authenticated API
	if len(openAPIs) != len(allAPIs) && n.config.NodeCfg.AuthRPC {
		jwtSecret, err := n.obtainJWTSecret(n.config.NodeCfg.JWTSecret)
		if err != nil {
			return err
		}
		config := httpConfig{
			CorsAllowedOrigins: utils.SplitAndTrim(n.config.NodeCfg.HTTPCors),
			Vhosts:             []string{"*"},
			Modules:            []string{"apos"},
			prefix:             "",
			jwtSecret:          jwtSecret,
		}

		if err := n.httpAuth.setListenAddr(n.config.NodeCfg.AuthAddr, n.config.NodeCfg.AuthPort); err != nil {
			return err
		}
		if err := n.httpAuth.enableRPC(n.rpcAPIs, config); err != nil {
			return err
		}
		if err := n.httpAuth.start(); err != nil {
			return err
		}
	}

	return nil
}

func (n *Node) stopRPC() {
	n.http.stop()
	n.httpAuth.stop()
	n.ws.stop()
	n.ipc.stop()
	n.stopInProc()
	if n.rateLimiter != nil {
		n.rateLimiter.Stop()
	}
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
		// 3b. History expirer
		{"History expirer", func() error {
			if n.historyExpirer != nil {
				n.historyExpirer.Stop()
			}
			return nil
		}},
		// 3c. Transaction generator
		{"Transaction generator", func() error {
			if n.txGenerator != nil {
				n.txGenerator.Stop()
			}
			return nil
		}},
		// 4. Miner
		{"Miner", func() error { n.miner.Close(); return nil }},
		// 5. Snap sync + Initial sync (depends on P2P + blockchain, must stop before blockchain closes)
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
		// 9. Consensus engine
		{"Consensus engine", func() error { return n.engine.Close() }},
		// 10. ExEx manager (stop extensions before blockchain closes)
		{"ExEx manager", func() error {
			if n.exexManager != nil {
				n.exexManager.Stop()
			}
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
		{"P2P network", func() error { return n.p2p.Stop() }},
	}

	for i, svc := range services {
		log.PrintShutdownStep(i+1, len(services), svc.name)
		if err := svc.closer(); err != nil {
			errs = append(errs, err)
			log.PrintSubItem(fmt.Sprintf("%s stopped with error: %v", svc.name, err))
		}
	}

	return errs
}

// doClose releases resources acquired by New(), collecting errors.
func (n *Node) doClose(errs []error) error {
	// Close databases. This needs the lock because it needs to
	// synchronize with OpenDatabase*.
	n.lock.Lock()
	n.state = closedState
	n.db.Close()
	n.lock.Unlock()

	if err := n.accman.Close(); err != nil {
		errs = append(errs, err)
	}
	if n.keyDirTemp {
		if err := os.RemoveAll(n.keyDir); err != nil {
			errs = append(errs, err)
		}
	}

	// Release instance directory lock.
	n.closeDataDir()

	// Unblock n.Wait.
	close(n.shutDown)

	return errors.Join(errs...)
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

// getKeyStoreDir retrieves the key directory and will create
// and ephemeral one if necessary.
func getKeyStoreDir(conf *conf.NodeConfig) (string, bool, error) {
	keydir, err := conf.KeyDirConfig()
	if err != nil {
		return "", false, err
	}
	isEphemeral := false
	if keydir == "" {
		// There is no datadir.
		keydir, err = os.MkdirTemp("", "go-ethereum-keystore")
		isEphemeral = true
	}

	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(keydir, 0700); err != nil {
		return "", false, err
	}

	return keydir, isEphemeral, nil
}

func setAccountManagerBackends(stack *Node, conf *conf.NodeConfig) error {
	am := stack.AccountManager()
	keydir := stack.KeyStoreDir()
	scryptN := keystore.StandardScryptN
	scryptP := keystore.StandardScryptP
	if conf.UseLightweightKDF {
		scryptN = keystore.LightScryptN
		scryptP = keystore.LightScryptP
	}

	// For now, we're using EITHER external signer OR local signers.
	// If/when we implement some form of lockfile for USB and keystore wallets,
	// we can have both, but it's very confusing for the user to see the same
	// accounts in both externally and locally, plus very racey.
	am.AddBackend(keystore.NewKeyStore(keydir, scryptN, scryptP))

	return nil
}

// KeyStoreDir retrieves the key directory
func (n *Node) KeyStoreDir() string {
	return n.keyDir
}

func (n *Node) SetupMetrics(config conf.MetricsConfig) {
	if !config.Enable {
		return
	}

	// Register Go runtime and system-level metrics.
	nodeMetrics.RegisterSystemMetrics()

	if config.HTTP != "" {
		address := net.JoinHostPort(config.HTTP, strconv.Itoa(config.Port))
		log.Info("Enabling stand-alone metrics HTTP endpoint", "address", address)
		prometheus.Setup(address, log.Root())
	} else if config.Port != 0 {
		log.Warn("--metrics.port specified without --metrics.addr, metrics server will not start.")
	}
}

func (s *Node) Etherbase() (eb types.Address, err error) {
	s.lock.RLock()
	etherbase := s.etherbase
	s.lock.RUnlock()

	if etherbase != (types.Address{}) {
		return etherbase, nil
	}
	if wallets := s.AccountManager().Wallets(); len(wallets) > 0 {
		if accounts := wallets[0].Accounts(); len(accounts) > 0 {
			etherbase := accounts[0].Address

			s.lock.Lock()
			s.etherbase = etherbase
			s.lock.Unlock()

			log.Info("Etherbase automatically configured", "address", etherbase)
			return etherbase, nil
		}
	}
	return types.Address{}, errors.New("etherbase must be explicitly specified")
}

func OpenDatabase(ctx context.Context, cfg *conf.Config, logger log2.Logger, name string) (kv.RwDB, error) {
	if cfg.NodeCfg.DataDir == "" {
		return memdb.New(""), nil
	}

	if logger == nil {
		logger = log2.New()
	}

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	// If layered DB is enabled, split into state + history databases.
	if cfg.LayeredDBCfg.Enable {
		return openLayeredDatabase(ctx, cfg, logger, name)
	}

	dbPath := filepath.Join(cfg.NodeCfg.DataDir, name)
	log.Info("Opening database", "name", name, "path", dbPath)

	roTxsLimiter := semaphore.NewWeighted(int64(cmp.Max(32, runtime.GOMAXPROCS(-1)*8)))

	chainKv, err := mdbx.NewMDBX(logger).
		WriteMergeThreshold(4 * 8192).
		Path(dbPath).Label(kv.ChainDB).
		DBVerbosity(kv.DBVerbosityLvl(2)).RoTxsLimiter(roTxsLimiter).
		MapSize(8 * datasize.TB).
		Open(ctx)
	if err != nil {
		return nil, err
	}

	if err = chainKv.Update(ctx, func(tx kv.RwTx) error {
		return params.SetN42Version(tx, params.VersionKeyCreated)
	}); err != nil {
		return nil, err
	}
	return chainKv, nil
}

// openLayeredDatabase creates a LayeredDB with separate state and history
// MDBX instances. The state DB is smaller and faster (holds only current
// state), while the history DB handles append-heavy changeset/index data.
func openLayeredDatabase(ctx context.Context, cfg *conf.Config, logger log2.Logger, name string) (kv.RwDB, error) {
	if err := cfg.LayeredDBCfg.Validate(); err != nil {
		return nil, fmt.Errorf("layered DB config: %w", err)
	}

	stateDBPath := cfg.LayeredDBCfg.StateDBPath
	if stateDBPath == "" {
		stateDBPath = filepath.Join(cfg.NodeCfg.DataDir, name+"-state")
	}
	historyDBPath := cfg.LayeredDBCfg.HistoryDBPath
	if historyDBPath == "" {
		historyDBPath = filepath.Join(cfg.NodeCfg.DataDir, name+"-history")
	}

	log.Info("Opening layered database",
		"stateDB", stateDBPath,
		"historyDB", historyDBPath,
		"cacheShards", cfg.LayeredDBCfg.CacheShards,
		"cacheCapacity", cfg.LayeredDBCfg.CacheCapacity,
	)

	roTxsLimiter := semaphore.NewWeighted(int64(cmp.Max(32, runtime.GOMAXPROCS(-1)*8)))

	// State DB: smaller MapSize, optimized for random read/write.
	stateKv, err := mdbx.NewMDBX(logger).
		WriteMergeThreshold(4 * 8192).
		Path(stateDBPath).Label(kv.ChainDB).
		DBVerbosity(kv.DBVerbosityLvl(2)).RoTxsLimiter(roTxsLimiter).
		MapSize(2 * datasize.TB).
		Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("open state DB: %w", err)
	}

	// History DB: larger MapSize, optimized for sequential append.
	historyKv, err := mdbx.NewMDBX(logger).
		WriteMergeThreshold(4 * 8192).
		Path(historyDBPath).Label(kv.ChainDB).
		DBVerbosity(kv.DBVerbosityLvl(2)).RoTxsLimiter(roTxsLimiter).
		MapSize(8 * datasize.TB).
		Open(ctx)
	if err != nil {
		stateKv.Close()
		return nil, fmt.Errorf("open history DB: %w", err)
	}

	db := layered.NewLayeredDB(stateKv, historyKv, &cfg.LayeredDBCfg)

	if err = db.Update(ctx, func(tx kv.RwTx) error {
		return params.SetN42Version(tx, params.VersionKeyCreated)
	}); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

func WriteGenesisBlock(db kv.RwTx, genesis *conf.Genesis) (*block.Block, error) {
	if genesis == nil {
		return nil, internal.ErrGenesisNoConfig
	}

	g := &internal.GenesisBlock{
		Hash:          "",
		GenesisConfig: genesis,
	}
	log.Info("Initializing genesis block...")
	genBlock, _, err := g.Write(db)
	if err != nil {
		return nil, err
	}
	return genBlock, nil
}

func WriteChainConfig(db kv.RwTx, genesisHash types.Hash, genesis *conf.Genesis) error {
	if err := rawdb.WriteChainConfig(db, genesisHash, genesis.Config); err != nil {
		log.Error("failed to write chain config", "err", err)
		return err
	}
	return nil
}

func SplitTagsFlag(tagsFlag string) map[string]string {
	tags := strings.Split(tagsFlag, ",")
	tagsMap := map[string]string{}

	for _, t := range tags {
		if t != "" {
			kv := strings.Split(t, "=")

			if len(kv) == 2 {
				tagsMap[kv[0]] = kv[1]
			}
		}
	}

	return tagsMap
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

// =============================================================================
// API backend adapters
// =============================================================================

// p2pAdminAdapter bridges p2p.P2P to the api.P2PAdmin interface so that
// admin_* RPC methods can return real peer data without the api package
// importing the p2p package directly.
type p2pAdminAdapter struct {
	svc p2p.P2P
}

func (a *p2pAdminAdapter) PeerInfos() []*api.PeerInfo {
	status := a.svc.Peers()
	connected := status.Connected()
	result := make([]*api.PeerInfo, 0, len(connected))
	for _, pid := range connected {
		info := &api.PeerInfo{
			ID:   pid.String(),
			Name: "N42",
			Caps: []string{"eth/69"},
		}
		if addr, err := status.Address(pid); err == nil {
			info.Network.RemoteAddress = addr.String()
		}
		result = append(result, info)
	}
	return result
}

func (a *p2pAdminAdapter) SelfNodeID() string {
	return a.svc.PeerID().String()
}

func (a *p2pAdminAdapter) SelfENR() string {
	record := a.svc.ENR()
	if record == nil {
		return ""
	}
	s, err := p2p.SerializeENR(record)
	if err != nil {
		return ""
	}
	return s
}

func (a *p2pAdminAdapter) SelfListenAddrs() []string {
	h := a.svc.Host()
	if h == nil {
		return nil
	}
	addrs := h.Addrs()
	result := make([]string, len(addrs))
	for i, ma := range addrs {
		result[i] = ma.String()
	}
	return result
}

func (a *p2pAdminAdapter) AddPeer(addr string) error {
	ma, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		return fmt.Errorf("admin_addPeer: invalid multiaddr %q: %w", addr, err)
	}
	info, err := libp2ppeer.AddrInfoFromP2pAddr(ma)
	if err != nil {
		return fmt.Errorf("admin_addPeer: cannot derive peer info from %q: %w", addr, err)
	}
	// Service.Connect wraps host.Connect with the service context.
	type connector interface{ Connect(libp2ppeer.AddrInfo) error }
	if c, ok := a.svc.(connector); ok {
		return c.Connect(*info)
	}
	return a.svc.Host().Connect(context.Background(), *info)
}

func (a *p2pAdminAdapter) RemovePeer(peerID string) error {
	pid, err := libp2ppeer.Decode(peerID)
	if err != nil {
		return fmt.Errorf("admin_removePeer: invalid peer ID %q: %w", peerID, err)
	}
	return a.svc.Disconnect(pid)
}

func (a *p2pAdminAdapter) HighestPeerBlock() uint64 {
	return a.svc.Peers().HighestBlockNumber().Uint64()
}

// minerAdminAdapter bridges *miner.Miner to api.MinerAdmin.
type minerAdminAdapter struct {
	m *miner.Miner
}

func (a *minerAdminAdapter) Mining() bool {
	return a.m.Mining()
}

func (a *minerAdminAdapter) SetCoinbase(addr types.Address) {
	a.m.SetCoinbase(addr)
}

// nodeHealthProvider implements HealthProvider for the Node.
type nodeHealthProvider struct {
	node *Node
}

func (h *nodeHealthProvider) CurrentBlock() uint64 {
	if h.node.blockChain == nil {
		return 0
	}
	b := h.node.blockChain.CurrentBlock()
	if b == nil {
		return 0
	}
	return b.Number64().Uint64()
}

func (h *nodeHealthProvider) HighestBlock() uint64 {
	if h.node.p2p == nil {
		return h.CurrentBlock()
	}
	highest := h.node.p2p.Peers().HighestBlockNumber().Uint64()
	if current := h.CurrentBlock(); current > highest {
		return current
	}
	return highest
}

func (h *nodeHealthProvider) IsSyncing() bool {
	if h.node.is == nil {
		return false
	}
	return h.node.is.Syncing()
}

func (h *nodeHealthProvider) PeerCount() int {
	if h.node.p2p == nil {
		return 0
	}
	return len(h.node.p2p.Peers().Connected())
}

// nodeBlockProvider implements snapshot.BlockProvider for the Node.
type nodeBlockProvider struct {
	node *Node
}

func (b *nodeBlockProvider) CurrentBlock() uint64 {
	if b.node.blockChain == nil {
		return 0
	}
	blk := b.node.blockChain.CurrentBlock()
	if blk == nil {
		return 0
	}
	return blk.Number64().Uint64()
}

// devnetGenesisBlock creates a minimal genesis for --dev / private chain mode.
// It uses Clique (APoa) consensus with a 4-second block period and pre-funds
// the configured etherbase (if any) so the node can immediately start mining.
func devnetGenesisBlock(cfg *conf.Config) *conf.Genesis {
	period := uint64(4)
	if cfg.ChainCfg != nil && cfg.ChainCfg.Clique != nil && cfg.ChainCfg.Clique.Period > 0 {
		period = cfg.ChainCfg.Clique.Period
	}

	zero := big.NewInt(0)
	chainConfig := &params.ChainConfig{
		ChainID:               big.NewInt(94),
		HomesteadBlock:        zero,
		TangerineWhistleBlock: zero,
		SpuriousDragonBlock:   zero,
		ByzantiumBlock:        zero,
		ConstantinopleBlock:   zero,
		PetersburgBlock:       zero,
		IstanbulBlock:         zero,
		MuirGlacierBlock:      zero,
		BerlinBlock:           zero,
		LondonBlock:           zero,
		ArrowGlacierBlock:     zero,
		Consensus:             params.CliqueConsensus,
		Clique: &params.CliqueConfig{
			Period: period,
			Epoch:  30000,
		},
	}

	alloc := make(conf.GenesisAlloc)

	// Pre-fund etherbase with a large balance.
	if cfg.Miner.Etherbase != "" {
		addr := types.HexToAddress(cfg.Miner.Etherbase)
		alloc[addr] = conf.GenesisAccount{
			Balance: "1000000000000000000000000000", // 10^27 wei
		}
	}

	// Pre-fund deterministic stress-test accounts so cmd/stresstest works out of the box.
	// Accounts are generated as: sha256("n42-stress-test-key-{i}") → ECDSA key → address.
	for i := 0; i < 50; i++ {
		seed := fmt.Sprintf("n42-stress-test-key-%d", i)
		h := sha256.Sum256([]byte(seed))
		key, err := crypto.ToECDSA(h[:])
		if err != nil {
			continue
		}
		addr := crypto.PubkeyToAddress(key.PublicKey)
		alloc[types.Address(addr)] = conf.GenesisAccount{
			Balance: "1000000000000000000000", // 1000 ETH each
		}
	}

	miners := []string{}
	if cfg.Miner.Etherbase != "" {
		miners = append(miners, cfg.Miner.Etherbase)
	}

	return &conf.Genesis{
		Config: chainConfig,
		Alloc:  alloc,
		Miners: miners,
	}
}
