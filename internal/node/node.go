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
	"errors"
	"fmt"
	"hash/crc32"
	"net"
	"path"
	"runtime"
	"strings"

	"github.com/gofrs/flock"
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/lib/common/cmp"
	"github.com/n42blockchain/N42/common/hexutil"
	prometheus "github.com/n42blockchain/N42/common/metrics"
	"github.com/n42blockchain/N42/contracts/deposit"
	n42deposit "github.com/n42blockchain/N42/contracts/deposit/AMT"
	fujideposit "github.com/n42blockchain/N42/contracts/deposit/FUJI"
	nftdeposit "github.com/n42blockchain/N42/contracts/deposit/NFT"
	"github.com/n42blockchain/N42/internal/debug"
	"github.com/n42blockchain/N42/internal/p2p"
	n42sync "github.com/n42blockchain/N42/internal/sync"
	initialsync "github.com/n42blockchain/N42/internal/sync/initial-sync"
	"github.com/n42blockchain/N42/internal/tracers"
	pkgerrors "github.com/pkg/errors"
	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/api"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"golang.org/x/sync/semaphore"

	"os"
	"path/filepath"
	"strconv"

	"github.com/n42blockchain/N42/log"

	"sync"

	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/accounts/keystore"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"

	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/apoa"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/internal/miner"
	"github.com/n42blockchain/N42/internal/txgen"
	"github.com/n42blockchain/N42/internal/txspool"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
	"github.com/n42blockchain/N42/utils"
	"go.uber.org/zap"
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

	// s
	miner           *miner.Miner
	blockChain      common.IBlockChain
	engine          consensus.Engine
	db              kv.RwDB
	txspool         common.ITxsPool
	depositContract *deposit.Deposit
	p2p             p2p.P2P
	sync            *n42sync.Service
	is              *initialsync.Service
	accman          *accounts.Manager

	api     *api.API
	rpcAPIs []jsonrpc.API

	http          *httpServer
	ipc           *ipcServer
	ws            *httpServer
	httpAuth      *httpServer //
	wsAuth        *httpServer //
	inprocHandler *jsonrpc.Server

	keyDir     string // key store directory
	keyDirTemp bool   // If true, key directory will be removed by Stop

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

	//
	chainKv, err = OpenDatabase(ctx, cfg, nil, kv.ChainDB.String())
	if err != nil {
		return nil, err
	}

	if err := chainKv.View(ctx, func(tx kv.Tx) error {
		//
		genesisHash, err = rawdb.ReadCanonicalHash(tx, 0)
		//
		if genesisHash == (types.Hash{}) && err != nil {
			//return fmt.Errorf("GenesisHash is missing err:%w", err)
			return internal.ErrGenesisNoConfig
		}
		if genesisHash == (types.Hash{}) && err == nil {
			//needs WriteGenesisBlock
			return nil
		}
		//
		chainConfig, err = rawdb.ReadChainConfig(tx, genesisHash)
		if err != nil {
			return err
		}
		//
		if genesisBlock, err = rawdb.ReadBlockByHash(tx, genesisHash); genesisBlock == nil {
			return fmt.Errorf("genesisBlock is missing err:%w", err)
		}

		return nil
	}); err != nil {
		return nil, err
	}

	if genesisHash == (types.Hash{}) {
		genesisHash = *params.GenesisHashByChainName(cfg.NodeCfg.Chain)
		genesisConfig = internal.GenesisByChainName(cfg.NodeCfg.Chain)
		chainConfig = params.ChainConfigByChainName(cfg.NodeCfg.Chain)
		if err := chainKv.Update(ctx, func(tx kv.RwTx) error {
			var genesisErr error
			genesisBlock, genesisErr = WriteGenesisBlock(tx, genesisConfig)
			if nil != genesisErr {
				return genesisErr
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}

	// update ChainConfig everytime
	if cfg.NodeCfg.Chain != "private" {
		if err := chainKv.Update(ctx, func(tx kv.RwTx) error {
			genesisHash = *params.GenesisHashByChainName(cfg.NodeCfg.Chain)
			genesisConfig = internal.GenesisByChainName(cfg.NodeCfg.Chain)
			if err := WriteChainConfig(tx, genesisHash, genesisConfig); err != nil {
				return err
			}
			return nil
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

	if cfg.ChainCfg.Apos != nil {
		depositContracts := make(map[types.Address]deposit.DepositContract, 0)
		if depositContractAddress := cfg.ChainCfg.Apos.DepositContract; depositContractAddress != "" {
			var addr types.Address
			if !addr.DecodeString(depositContractAddress) {
				return nil, fmt.Errorf("cannot decode DepositContract address: %s", depositContractAddress)
			}
			depositContracts[addr] = new(n42deposit.Contract)
		}
		if depositNFTContractAddress := cfg.ChainCfg.Apos.DepositNFTContract; depositNFTContractAddress != "" {
			var addr types.Address
			if !addr.DecodeString(depositNFTContractAddress) {
				return nil, fmt.Errorf("cannot decode DepositNFTContract address: %s", depositNFTContractAddress)
			}
			depositContracts[addr] = new(nftdeposit.Contract)
		}

		if depositFUJIContractAddress := cfg.ChainCfg.Apos.DepositFUJIContract; depositFUJIContractAddress != "" {
			var addr types.Address
			if !addr.DecodeString(depositFUJIContractAddress) {
				return nil, fmt.Errorf("cannot decode DepositFUJIContract address: %s", depositFUJIContractAddress)
			}
			depositContracts[addr] = new(fujideposit.Contract)
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

	syncServer, err := n42sync.NewService(
		ctx,
		n42sync.WithP2P(p2p),
		n42sync.WithChainService(bc),
		n42sync.WithInitialSync(is),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create sync service: %w", err)
	}

	// Initialize bloom filter with pending transactions
	var txs []*transaction.Transaction
	pending := pool.Pending(false)
	for _, batch := range pending {
		txs = append(txs, batch...)
	}
	var bloom *types.Bloom
	if len(txs) > 0 {
		bloom, _ = types.NewBloom(uint64(len(txs)))
		for _, tx := range txs {
			hash := tx.Hash()
			bloom.Add(hash.Bytes())
		}
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

		p2p:  p2p,
		sync: syncServer,
		is:   is,
	}

	// Apply flags.
	//SetNodeConfig(ctx, &cfg)
	// Node doesn't by default populate account manager backends
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

	// Print the pretty banner
	log.PrintBanner(
		params.VersionWithMeta,
		fmt.Sprintf("%s (ID: %s)", chainName, cfg.ChainCfg.ChainID.String()),
		consensusName,
		actualGenesisHash.String(),
		bc.CurrentBlock().Number64().Uint64(),
	)

	// Check genesis hash mismatch
	if actualGenesisHash != expectedGenesisHash {
		log.PrintError(fmt.Sprintf("Genesis hash mismatch! Expected: %s", expectedGenesisHash.String()[:20]+"..."))
	}

	node.api = api.NewAPI(bc, chainKv, engine, pool, node.AccountManager(), cfg.ChainCfg)
	node.api.SetGpo(api.NewOracle(bc, miner, cfg.ChainCfg, gpoParams))
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

	if err := n.blockChain.Start(); err != nil {
		log.Errorf("failed setup blockChain service, err: %v", err)
		return err
	}

	if n.config.NodeCfg.Miner {

		// Configure the local mining address
		eb, err := n.Etherbase()
		if err != nil {
			log.Error("Cannot start mining without etherbase", "err", err)
			return fmt.Errorf("etherbase missing: %v", err)
		}

		if poa, ok := n.engine.(*apoa.Apoa); ok {
			wallet, err := n.accman.Find(accounts.Account{Address: eb})
			if wallet == nil || err != nil {
				log.Error("Etherbase account unavailable locally", "err", err)
				return fmt.Errorf("signer missing: %v", err)
			}
			poa.Authorize(eb, wallet.SignData)
		} else if pos, ok := n.engine.(*apos.APos); ok {
			wallet, err := n.accman.Find(accounts.Account{Address: eb})
			if wallet == nil || err != nil {
				log.Error("Etherbase account unavailable locally", "err", err)
				return fmt.Errorf("signer missing: %v", err)
			}
			pos.Authorize(eb, wallet.SignData)
		}

		n.miner.SetCoinbase(eb)
		n.miner.Start()
	}

	if pos, ok := n.engine.(*apos.APos); ok {
		pos.SetBlockChain(n.blockChain)
	}

	n.rpcAPIs = append(n.rpcAPIs, n.engine.APIs(n.blockChain)...)
	n.rpcAPIs = append(n.rpcAPIs, n.api.Apis()...)
	n.rpcAPIs = append(n.rpcAPIs, tracers.APIs(n.api)...)
	n.rpcAPIs = append(n.rpcAPIs, debug.APIs()...)

	if err := n.startRPC(); err != nil {
		log.Error("failed start jsonrpc service", zap.Error(err))
		return err
	}

	//n.p2p.AddConnectionHandler()
	n.p2p.Start()
	n.sync.Start()

	n.SetupMetrics(n.config.MetricsCfg)

	if n.depositContract != nil {
		n.depositContract.Start()
	}

	go n.is.Start()

	// Start transaction generator if enabled
	if n.config.DevCfg.TxGenEnabled {
		n.startTxGenerator()
	}

	log.Debug("node setup success!")

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

	if n.ipc.endpoint != "" {
		//if err := n.ipc.start(n.rpcAPIs); err != nil {
		//	return err
		//}
	}
	if n.config.NodeCfg.HTTP {
		//todo []string{"eth", "web3", "debug", "net", "apoa", "txpool", "apos"}
		config := httpConfig{
			CorsAllowedOrigins: utils.SplitAndTrim(n.config.NodeCfg.HTTPCors),
			Vhosts:             []string{"*"},
			Modules:            utils.SplitAndTrim(n.config.NodeCfg.HTTPApi),
			prefix:             "",
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
	n.ws.stop()
	n.ipc.stop()
	n.stopInProc()
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
	var errs []error

	services := []namedCloser{
		// 1. RPC services (depends on everything, stop first)
		{"RPC services", func() error { n.stopRPC(); return nil }},
		// 2. Transaction generator
		{"Transaction generator", func() error {
			if n.txGenerator != nil {
				n.txGenerator.Stop()
			}
			return nil
		}},
		// 3. Miner
		{"Miner", func() error { n.miner.Close(); return nil }},
		// 4. Initial sync (depends on P2P + blockchain, must stop before blockchain closes)
		{"Initial sync", func() error { return n.is.Stop() }},
		// 5. Sync service (depends on P2P + blockchain, must stop before blockchain closes)
		{"Sync service", func() error { return n.sync.Stop() }},
		// 6. Transaction pool
		{"Transaction pool", func() error { return n.txspool.Stop() }},
		// 7. Deposit contract
		{"Deposit contract", func() error {
			if n.depositContract != nil {
				return n.depositContract.Stop()
			}
			return nil
		}},
		// 8. Consensus engine
		{"Consensus engine", func() error { return n.engine.Close() }},
		// 9. Blockchain (flush and close DB, after all consumers stopped)
		{"Blockchain", func() error { return n.blockChain.Close() }},
		// 10. P2P networking (transport layer, last to go)
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

	// Report any errors that might have occurred.
	switch len(errs) {
	case 0:
		return nil
	case 1:
		return errs[0]
	default:
		return fmt.Errorf("%v", errs)
	}
}

func (n *Node) Wait() {
	<-n.shutDown
}

// AccountManager retrieves the account manager used by the protocol stack.
func (n *Node) AccountManager() *accounts.Manager {
	return n.accman
}

// AccountManager retrieves the account manager used by the protocol stack.
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
	if config.Enable {
		if config.HTTP != "" {
			address := net.JoinHostPort(config.HTTP, fmt.Sprintf("%d", config.Port))
			log.Info("Enabling stand-alone metrics HTTP endpoint", "address", address)
			prometheus.Setup(address, log.Root())
		} else if config.Port != 0 {
			log.Warn(fmt.Sprintf("--%s specified without --%s, metrics server will not start.", "metrics.port", "metrics.addr"))
		}
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

	var chainKv kv.RwDB
	var err error

	dbPath := filepath.Join(cfg.NodeCfg.DataDir, name)

	var openFunc func(exclusive bool) (kv.RwDB, error)
	log.Info("Opening database", "name", name, "path", dbPath)
	openFunc = func(exclusive bool) (kv.RwDB, error) {
		roTxsLimiter := semaphore.NewWeighted(int64(cmp.Max(32, runtime.GOMAXPROCS(-1)*8))) // 1 less than max to allow unlocking to happen
		opts := mdbx.NewMDBX(logger).
			WriteMergeThreshold(4 * 8192).
			Path(dbPath).Label(kv.ChainDB).
			DBVerbosity(kv.DBVerbosityLvl(2)).RoTxsLimiter(roTxsLimiter)
		if exclusive {
			opts = opts.Exclusive()
		}

		modules.N42Init()
		kv.ChaindataTablesCfg = modules.N42TableCfg

		opts = opts.MapSize(8 * datasize.TB)
		return opts.Open(ctx)
	}
	chainKv, err = openFunc(false)
	if err != nil {
		return nil, err
	}

	if err = chainKv.Update(ctx, func(tx kv.RwTx) (err error) {
		return params.SetN42Version(tx, params.VersionKeyCreated)
	}); err != nil {
		return nil, err
	}
	return chainKv, nil
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
		log.Error("cannot get chain config from db", "err", err)
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
