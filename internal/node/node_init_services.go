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

// NewNode phases: transaction pool, sync, miner, accounts, Node assembly,
// tracing, API and optional services.

package node

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/blockhashindex"
	"github.com/n42blockchain/N42/internal/bundler"
	"github.com/n42blockchain/N42/internal/cs"
	"github.com/n42blockchain/N42/internal/miner"
	"github.com/n42blockchain/N42/internal/peerdas"
	"github.com/n42blockchain/N42/internal/stateless"
	n42sync "github.com/n42blockchain/N42/internal/sync"
	"github.com/n42blockchain/N42/internal/sync/checkpoint"
	initialsync "github.com/n42blockchain/N42/internal/sync/initialsync"
	"github.com/n42blockchain/N42/internal/sync/snapsync"
	"github.com/n42blockchain/N42/internal/tracing"
	"github.com/n42blockchain/N42/internal/txlookup"
	"github.com/n42blockchain/N42/internal/txspool"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

// initTxPool resolves the deposit contract and creates the transaction pool.
func (n *Node) initTxPool(b *nodeBuild) error {
	cfg, ctx := b.cfg, b.ctx
	var err error
	b.depositContract, err = resolveDepositContract(ctx, b.bc, b.chainKv, cfg.ChainCfg)
	if err != nil {
		return err
	}

	// The pool used to read a package-level default that nothing could reach,
	// so its capacity was fixed at roughly 6,000 transactions regardless of
	// configuration -- smaller than one block above a ~126M gas ceiling, and
	// therefore an invisible ceiling on throughput.
	poolCfg := cfg.TxPoolCfg
	if fixed := poolCfg.Sanitize(); len(fixed) > 0 {
		log.Warn("Transaction pool config had unusable values, defaults applied", "fields", fixed)
	}
	b.pool, err = txspool.NewTxsPoolWithConfig(ctx, b.bc, b.depositContract, txspool.FromConf(poolCfg))
	if err != nil {
		return fmt.Errorf("failed to create transaction pool: %w", err)
	}
	log.Info("Transaction pool configured",
		"globalSlots", poolCfg.GlobalSlots, "globalQueue", poolCfg.GlobalQueue,
		"accountSlots", poolCfg.AccountSlots, "accountQueue", poolCfg.AccountQueue,
		"lifetime", poolCfg.Lifetime)

	// Block import reuses the pool's already-recovered senders instead of
	// re-deriving every signature (see SenderHintSource).
	if realBC, ok := b.bc.(*internal.BlockChain); ok {
		realBC.SetSenderHintSource(b.pool)
	}
	return nil
}

// initSyncServices creates initial, snap and checkpoint sync and the sync service.
func (n *Node) initSyncServices(b *nodeBuild) error {
	cfg, ctx := b.cfg, b.ctx
	var err error
	b.is = initialsync.NewService(ctx, &initialsync.Config{
		Chain: b.bc,
		P2P:   b.p2p,
	})

	b.snapSyncSvc = snapsync.NewService(ctx, &snapsync.Config{
		P2P:      b.p2p,
		Chain:    b.bc,
		DB:       b.chainKv,
		SnapSync: &cfg.SnapSyncCfg,
	})

	b.checkpointSvc = checkpoint.NewService(&checkpoint.Config{
		Checkpoint: &cfg.CheckpointCfg,
		P2P:        b.p2p,
		Chain:      b.bc,
		DB:         b.chainKv,
	})

	// Peers older than the RLP block wire format send protobuf. Reconstructing
	// from protobuf drops any header field types_pb has no slot for, which
	// changes the block hash, so it is only allowed on chains that never carry
	// one. See sync.AllowLegacyProtoBlocks.
	n42sync.AllowLegacyProtoBlocks(b.chainConfig)

	syncOpts := []n42sync.Option{
		n42sync.WithP2P(b.p2p),
		n42sync.WithChainService(b.bc),
		n42sync.WithInitialSync(b.is),
	}
	// Keep the sync/status-handshake genesis hash consistent with the P2P fork
	// digest (p2pGenesisHash already folds in the chain constant and any
	// --p2p.genesis-override). Advertising two different hashes would cause peers
	// to reject us with a wrong-network goodbye.
	syncOpts = append(syncOpts, n42sync.WithOverrideGenesisHash(b.p2pGenesisHash))
	if cfg.P2PCfg.TxGossipEnabled && b.pool != nil {
		syncOpts = append(syncOpts, n42sync.WithTxPool(b.pool))
	}
	b.syncServer, err = n42sync.NewService(ctx, syncOpts...)
	if err != nil {
		return fmt.Errorf("failed to create sync service: %w", err)
	}
	return nil
}

// initMiner creates the miner and wires the ZK prover into it.
func (n *Node) initMiner(b *nodeBuild) error {
	cfg, ctx := b.cfg, b.ctx
	b.miner = miner.NewMiner(ctx, cfg, b.bc, b.engine, b.pool, nil)

	// Wire ZK prover service to the miner so it can submit proof requests
	// after producing blocks.
	if b.zkProverSvc != nil {
		b.miner.SetZKProverService(b.zkProverSvc)
	}
	return nil
}

// initAccounts resolves the keystore directory and creates the account manager.
func (n *Node) initAccounts(b *nodeBuild) error {
	cfg := b.cfg
	var err error
	b.keyDir, b.isEphem, err = getKeyStoreDir(&cfg.NodeCfg)
	if err != nil {
		return err
	}
	// Creates an empty AccountManager with no backends. Callers (e.g. cmd/n42)
	// are required to add the backends later on.
	b.accman = accounts.NewManager(&accounts.Config{InsecureUnlockAllowed: cfg.NodeCfg.InsecureUnlockAllowed})
	return nil
}

// initEngineStateAdapter builds the Engine API state adapter (ETH EL mode only).
func (n *Node) initEngineStateAdapter(b *nodeBuild) error {
	cfg := b.cfg
	if b.profile.IsEthereumEL() {
		var adapterFreezer *freezer.Freezer
		if realBC, ok := b.bc.(*internal.BlockChain); ok {
			if f, ok := realBC.Freezer().(*freezer.Freezer); ok {
				adapterFreezer = f
			}
		}
		b.engineStateAdapter = api.NewEngineStateAdapter(b.chainKv, adapterFreezer, cfg.ChainCfg, b.engine)

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
				b.engineStateAdapter.WithCSSource(src)
				m := warm.Meta()
				log.Info("CS warm tier enabled",
					"dir", dir, "base_block", m.BaseBlock,
					"head_block", m.HeadBlock, "keep_blocks", m.KeepBlocks)
			}
		}
	}
	return nil
}

// initNodeStruct assembles the Node from the build state.
func (n *Node) initNodeStruct(b *nodeBuild) error {
	cfg, ctx := b.cfg, b.ctx
	var err error
	*n = Node{
		cliCtx:          b.cliCtx,
		ctx:             ctx,
		cancel:          b.cancel,
		config:          cfg,
		profile:         b.profile,
		runtimePlan:     resolveAuxiliaryRuntimePlan(cfg, b.profile),
		miner:           b.miner,
		genesisBlock:    b.genesisBlock,
		blockChain:      b.bc,
		txIndexer:       b.nodeTxIndexer,
		eraStore:        b.nodeEraStore,
		db:              b.chainKv,
		frameDB:         b.frameKv,
		shutDown:        make(chan struct{}),
		txspool:         b.pool,
		engine:          b.engine,
		depositContract: b.depositContract,

		exexManager:     b.exexMgr,
		zkProverService: b.zkProverSvc,
		zkVerifier:      b.zkVerify,
		inprocHandler:   jsonrpc.NewServer(),
		http:            newHTTPServer(),
		ws:              newHTTPServer(),
		wsAuth:          newHTTPServer(),
		httpAuth:        newHTTPServer(),
		ipc:             newIPCServer(&cfg.NodeCfg),
		etherbase:       types.HexToAddress(cfg.Miner.Etherbase),

		accman:             b.accman,
		keyDir:             b.keyDir,
		keyDirTemp:         b.isEphem,
		engineStateAdapter: b.engineStateAdapter,

		p2pGenesisHash: b.p2pGenesisHash,
		p2p:            b.p2p,
		sync:           b.syncServer,
		is:             b.is,
		checkpointSync: b.checkpointSvc,
		snapSync:       b.snapSyncSvc,
	}

	if err = setAccountManagerBackends(n, &cfg.NodeCfg); err != nil {
		log.Errorf("Failed to set account manager backends: %v", err)
	}

	b.gpoParams = cfg.GPO
	if b.gpoParams.Default == nil {
		b.gpoParams.Default = cfg.Miner.GasPrice
	}
	return nil
}

// initBanner prints the startup banner and checks the genesis hash.
func (n *Node) initBanner(b *nodeBuild) error {
	cfg := b.cfg
	// Print beautiful startup banner
	actualGenesisHash := b.genesisBlock.Hash()
	expectedGenesisHash, hasExpectedGenesis := b.configuredGenesis.canonicalHash()

	// Get chain info from description
	chainName := cfg.NodeCfg.Chain
	if chainName == "" {
		chainName = "mainnet"
	}
	consensusName := cfg.ChainCfg.Consensus.DisplayName()

	// Print the pretty banner with system info
	currentBlockNumber := uint64(0)
	if currentBlock := b.bc.CurrentBlock(); currentBlock != nil {
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
	return nil
}

// initTracing initialises OpenTelemetry tracing.
func (n *Node) initTracing(b *nodeBuild) error {
	cfg := b.cfg
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
	n.tracingShutdown = tracingShutdown
	return nil
}

// initStateless enables stateless validation mode when configured.
func (n *Node) initStateless(b *nodeBuild) error {
	cfg := b.cfg
	// Stateless validation mode: validate blocks via Merkle proof witnesses only.
	if cfg.NodeCfg.StatelessEnabled {
		n.statelessMode = true
		n.statelessValidator = stateless.NewValidator(cfg.ChainCfg)
		log.Info("Stateless validation mode enabled — blocks will be verified via JMT witnesses",
			"codeCacheSize", 4096)
	}
	return nil
}

// initAPI creates the API backend and its optional cold lookup indexes.
func (n *Node) initAPI(b *nodeBuild) error {
	cfg := b.cfg
	n.api = api.NewAPI(b.bc, b.chainKv, b.engine, b.pool, n.AccountManager(), cfg.ChainCfg)
	n.api.SetRPCMaxGasPrice(cfg.NodeCfg.RPCMaxGasPrice)
	n.api.SetGpo(api.NewOracle(b.bc, b.miner, cfg.ChainCfg, b.gpoParams))
	n.api.SetP2P(&p2pAdminAdapter{svc: b.p2p, node: n})
	n.api.SetMiner(&minerAdminAdapter{m: b.miner})

	// Optional cold tx-hash → block index (txlookup RecSplit segments). Lets
	// eth_getTransactionByHash resolve historical txs that have aged out of the
	// MDBX TxLookup table. Opt-in via N42_TXINDEX_DIR (built by cmd/txindex-rebuild);
	// off by default so default nodes are unchanged.
	if dir := os.Getenv("N42_TXINDEX_DIR"); dir != "" {
		if svc, err := txlookup.NewService(dir); err != nil {
			log.Warn("txindex cold lookup: open failed", "dir", dir, "err", err)
		} else {
			n.api.SetTxIndexService(svc)
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
			n.api.SetBlockHashIndex(svc)
			log.Info("blockhash cold lookup enabled", "dir", dir, "segments", svc.SegmentCount())
		}
	}
	return nil
}

// initBundler creates the ERC-4337 bundler service when enabled.
func (n *Node) initBundler(b *nodeBuild) error {
	cfg := b.cfg
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
			return err
		}
		n.bundlerService = bundler.NewBundlerService(bundlerCfg, chainID)
	}
	return nil
}

// initPeerDAS creates the PeerDAS service when enabled.
func (n *Node) initPeerDAS(b *nodeBuild) error {
	cfg := b.cfg
	// Create PeerDAS service if enabled.
	if cfg.PeerDASCfg.IsEnabled() {
		peerID := n.p2p.PeerID()
		nodeIDBytes := []byte(peerID)
		peerdasCfg := peerdas.Config{
			Enable:          true,
			CustodyCount:    cfg.PeerDASCfg.CustodyCount,
			SamplingEnabled: cfg.PeerDASCfg.SamplingEnabled,
			SampleCount:     cfg.PeerDASCfg.SampleCount,
		}
		n.peerdasService = peerdas.NewService(peerdasCfg, nodeIDBytes, n.db)
		n.peerdasService.SetBlockProvider(&peerdasBlockProvider{node: n})
	}
	return nil
}
