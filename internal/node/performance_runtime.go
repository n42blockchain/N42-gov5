package node

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/params"
	"github.com/urfave/cli/v2"
)

const performanceRuntimeRoot = "/data/blockchain/gov5-performance-5m/runtime"

type performanceValidatorRuntime struct {
	replica   *sharedvalidator.Replica
	network   *sharedvalidator.Network
	producer  *sharedvalidator.Producer
	client    *sharedexec.Client
	public    *p2p.Service
	service   *hotstuff.Service
	engine    *hotstuff.HotStuff
	api       *sharedvalidator.API
	apiServer *http.Server
	apiDone   chan struct{}
	root      string
}

func (n *Node) IsPerformanceValidator() bool { return n.performanceValidator != nil }

func privatePerformancePath(path, root string, directory, socket bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return errors.New("performance runtime paths must be absolute and inside dedicated runtime root")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return errors.New("performance runtime path must exist without symlinks")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() || info.Mode().Perm()&0077 != 0 {
		return errors.New("performance runtime path must be private and owned by current user")
	}
	if directory {
		if !info.IsDir() {
			return errors.New("performance runtime directory required")
		}
	} else if socket {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("performance execution Unix socket required")
		}
	} else if !info.Mode().IsRegular() {
		return errors.New("performance private regular file required")
	}
	return nil
}

// root is fixed by the public Node constructor; tests supply their own temporary
// root while exercising this same constructor and lifecycle.
func newPerformanceValidatorNode(ctx context.Context, cancel context.CancelFunc, cliCtx *cli.Context, cfg *conf.Config, profile params.ProfileDescriptor, genesis configuredGenesis, root string) (_ *Node, err error) {
	if err := cfg.NodeCfg.ValidatePerformanceOwnerQueue(cfg.NodeCfg.Chain, profile); err != nil {
		return nil, err
	}
	if cfg.NodeCfg.PerformanceRole != "validator" || cfg.NodeCfg.PerformanceExecutionSocket == "" || cfg.NodeCfg.PerformanceGenesis == "" || cfg.NodeCfg.Chain != "private" || !profile.IsN42() || genesis.genesis == nil || genesis.chainConfig == nil || genesis.chainConfig.Performance == nil || genesis.chainConfig.HotStuff == nil || len(genesis.chainConfig.HotStuff.Validators) != 7 {
		return nil, errors.New("performance validator runtime requires explicit private+n42 performance genesis, seven validators and execution socket")
	}
	if cfg.NodeCfg.HTTP || cfg.NodeCfg.WS || cfg.NodeCfg.AuthRPC || cfg.NodeCfg.IPCPath != "" || cfg.NodeCfg.PrivateAPIAddr != "" {
		return nil, errors.New("performance metadata validator does not expose execution HTTP/WS/IPC/Engine/KV RPC")
	}
	if cfg.LayeredDBCfg.Enable || cfg.StorageTierCfg.Enabled || cfg.NodeCfg.AncientDB || cfg.NodeCfg.StatelessEnabled || cfg.NodeCfg.JMTCommitment {
		return nil, errors.New("performance metadata validator requires its dedicated plain metadata store")
	}
	if cfg.Miner.BlockIntervalMs > uint64((1<<63-1)/time.Millisecond) {
		return nil, errors.New("performance producer interval overflows duration")
	}
	if genesis.chainConfig.DeferredExecutionTime != nil || block.DeferredExecutionTime != 0 || genesis.chainConfig.HotStuff.InteropV4 || (genesis.chainConfig.HotStuff.CommitteePool != nil && genesis.chainConfig.HotStuff.CommitteePool.Enabled) {
		return nil, errors.New("performance validator deferred/domain adapter is not wired")
	}
	for _, entry := range os.Environ() {
		key := strings.SplitN(entry, "=", 2)[0]
		if strings.HasPrefix(key, "N42_") || strings.HasPrefix(key, "QS_") || strings.HasPrefix(key, "HIVE_") {
			return nil, fmt.Errorf("performance validator requires explicit configuration; remove inherited %s", key)
		}
	}
	// This constructor deliberately skips NewBlockChain. It must still select
	// the same process-local commitment rules before producing/validating bodies.
	if err := internal.ConfigureBlockCommitmentRules(genesis.chainConfig); err != nil {
		return nil, err
	}
	if err := privatePerformancePath(cfg.NodeCfg.DataDir, root, true, false); err != nil {
		return nil, err
	}
	if err := privatePerformancePath(filepath.Dir(cfg.NodeCfg.PerformanceExecutionSocket), root, true, false); err != nil {
		return nil, err
	}
	if err := privatePerformancePath(cfg.NodeCfg.PerformanceExecutionSocket, root, false, true); err != nil {
		return nil, err
	}
	keydir, err := cfg.NodeCfg.KeyDirConfig()
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(keydir, cfg.NodeCfg.DataDir+string(filepath.Separator)) {
		return nil, errors.New("performance validator key directory must be inside its own datadir")
	}
	if err := privatePerformancePath(keydir, root, true, false); err != nil {
		return nil, err
	}
	address := types.HexToAddress(cfg.Miner.Etherbase)
	if address == (types.Address{}) {
		return nil, errors.New("performance validator requires explicit nonzero etherbase")
	}
	var publicKey []byte
	for _, v := range genesis.chainConfig.HotStuff.Validators {
		if types.HexToAddress(v.Address) == address {
			publicKey, err = hex.DecodeString(strings.TrimPrefix(v.BLSKey, "0x"))
			if err != nil {
				return nil, err
			}
			break
		}
	}
	if len(publicKey) == 0 {
		return nil, errors.New("performance validator etherbase is not a genesis validator")
	}
	found := false
	for _, name := range []string{"bls_" + strings.ToLower(address.Hex()) + ".key", "bls_" + strings.TrimPrefix(strings.ToLower(address.Hex()), "0x") + ".key"} {
		path := filepath.Join(keydir, name)
		if _, e := os.Lstat(path); os.IsNotExist(e) {
			continue
		}
		if err := privatePerformancePath(path, root, false, false); err != nil {
			return nil, err
		}
		found = true
	}
	if !found {
		return nil, errors.New("performance validator requires its private BLS key file")
	}
	key, err := hotstuff.LoadBLSKeyFromDir(keydir, address)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(key.PublicKey().Marshal(), publicKey) {
		return nil, errors.New("performance validator BLS key does not match genesis identity")
	}
	if cfg.P2PCfg == nil || cfg.P2PCfg.PrivateKey == "" {
		return nil, errors.New("performance validator requires an explicitly provisioned P2P key")
	}
	if err := privatePerformancePath(cfg.P2PCfg.PrivateKey, root, false, false); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(cfg.P2PCfg.PrivateKey, cfg.NodeCfg.DataDir+string(filepath.Separator)) {
		return nil, errors.New("performance validator P2P key must be inside its own datadir")
	}
	if err := p2p.ValidatePerformanceP2PConfig(cfg.P2PCfg); err != nil {
		return nil, err
	}
	n := &Node{cliCtx: cliCtx, ctx: ctx, cancel: cancel, config: cfg, profile: profile, state: initializingState, shutDown: make(chan struct{}), keyDir: keydir, etherbase: address}
	rt := new(performanceValidatorRuntime)
	rt.root = root
	n.performanceValidator = rt
	defer func() {
		if err != nil {
			n.closePerformanceValidator()
		}
	}()
	if err = n.openDataDir(cfg); err != nil {
		return nil, err
	}
	if n.db, err = OpenDatabase(ctx, cfg, nil, kv.ChainDB.String()); err != nil {
		return nil, err
	}
	if err = n.db.View(ctx, func(tx kv.Tx) error {
		metadata, e := sharedvalidator.IsMetadataStore(tx)
		if e != nil {
			return e
		}
		if !metadata {
			return errors.New("performance validator requires explicit metadata-only init; execution stores cannot be reused")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	gb, _, err := (&internal.GenesisBlock{GenesisConfig: genesis.genesis}).ToBlock()
	if err != nil {
		return nil, err
	}
	n.genesisBlock, n.p2pGenesisHash = gb, gb.Hash()
	rt.engine = hotstuff.New(genesis.chainConfig.HotStuff, genesis.chainConfig)
	rt.engine.Authorize(address, key)
	if err = rt.engine.InitEngineFromConfig(); err != nil {
		return nil, err
	}
	n.engine = rt.engine
	rt.client = sharedexec.NewClient(cfg.NodeCfg.PerformanceExecutionSocket, sharedexec.Identity{Version: 1, Namespace: genesis.chainConfig.Performance.NetworkNamespace, ChainID: genesis.chainConfig.ChainID.String(), GenesisHash: gb.Hash()})
	if rt.replica, err = sharedvalidator.New(ctx, n.db, genesis.chainConfig, gb, rt.engine, rt.client); err != nil {
		return nil, err
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
	_, err = rt.client.AppliedChild(probeCtx, sharedexec.AppliedChildRequest{Identity: rt.client.Identity(), ParentHash: gb.Hash(), Number: 1, View: hotstuff.ViewNumber(^uint64(0))})
	probeCancel()
	// A synced owner can be beyond the retained lineage window. Fail closed
	// until historical catch-up evidence is implemented instead of hiding it.
	if err != nil {
		return nil, fmt.Errorf("performance owner identity/lineage probe: %w", err)
	}
	pcfg := *cfg.P2PCfg
	pcfg.DataDir = cfg.NodeCfg.DataDir
	if rt.public, err = p2p.NewService(ctx, gb.Hash(), &pcfg, cfg.NodeCfg, p2p.WithPerformanceChain(genesis.chainConfig)); err != nil {
		return nil, err
	}
	n.p2p = rt.public
	if rt.network, err = sharedvalidator.NewNetwork(ctx, rt.replica, rt.public); err != nil {
		return nil, err
	}
	gossip := fmt.Sprintf(p2p.HotStuffConsensusTopicFormat, utils.ToBytes4(n.p2pGenesisHash[:]))
	rt.service = hotstuff.NewService(rt.engine, newHotstuffP2PAdapter(rt.public), n.db, gossip, p2p.RPCHotStuffDirectTopicV1)
	if err = rt.network.AttachService(rt.service); err != nil {
		return nil, err
	}
	if rt.producer, err = sharedvalidator.NewProducer(ctx, rt.replica, time.Duration(cfg.Miner.BlockIntervalMs)*time.Millisecond); err != nil {
		return nil, err
	}
	if cfg.NodeCfg.PerformanceOwnerQueue {
		if err = rt.producer.UseOwnerQueue(ctx, sharedvalidator.OwnerQueueOptions{MaxTransactions: cfg.NodeCfg.PerformanceQueueMaxTransactions, MaxEncodedBytes: cfg.NodeCfg.PerformanceQueueMaxEncodedBytes}); err != nil {
			return nil, fmt.Errorf("performance owner queue startup: %w", err)
		}
	}
	if rt.api, err = sharedvalidator.NewAPI(rt.replica, rt.producer); err != nil {
		return nil, err
	}
	if cfg.NodeCfg.Miner {
		rt.service.SetBlockProducer(rt.producer)
	}
	cfg.ChainCfg = genesis.chainConfig
	n.hotstuffService = rt.service
	return n, nil
}

func (n *Node) startPerformanceValidator() error {
	n.lock.Lock()
	state := n.state
	n.lock.Unlock()
	if state == closedState {
		return ErrNodeStopped
	}
	if state == runningState {
		return ErrNodeRunning
	}
	rt := n.performanceValidator
	rt.public.Start()
	if !rt.public.Started() {
		n.closePerformanceValidator()
		return errors.New("performance P2P failed to start")
	}
	// Install the vote journal before either network imports or a producer
	// can feed consensus events. Fetch requests emitted by Service.Start can
	// queue safely until the metadata network starts below.
	if err := rt.service.Start(); err != nil {
		n.closePerformanceValidator()
		return err
	}
	if err := rt.network.Start(); err != nil {
		n.closePerformanceValidator()
		return err
	}
	dir := filepath.Join(n.config.NodeCfg.DataDir, "validator-rpc")
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		n.closePerformanceValidator()
		return err
	}
	if err := privatePerformancePath(dir, rt.root, true, false); err != nil {
		n.closePerformanceValidator()
		return err
	}
	server, listener, err := rt.api.ListenUnix(filepath.Join(dir, "validator.sock"))
	if err != nil {
		n.closePerformanceValidator()
		return err
	}
	rt.apiServer, rt.apiDone = server, make(chan struct{})
	go func() {
		defer close(rt.apiDone)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			n.cancel()
		}
	}()
	if n.config.NodeCfg.Miner {
		if err := rt.producer.Start(); err != nil {
			n.closePerformanceValidator()
			return err
		}
	}
	n.lock.Lock()
	n.state = runningState
	n.lock.Unlock()
	go func() { <-n.ctx.Done(); _ = n.Close() }()
	return nil
}

func (n *Node) closePerformanceValidator() error {
	n.lock.Lock()
	if n.state == closedState {
		n.lock.Unlock()
		return ErrNodeStopped
	}
	n.state = closedState
	n.lock.Unlock()
	n.cancel()
	rt := n.performanceValidator
	var errs []error
	if rt.apiServer != nil {
		if err := rt.apiServer.Close(); err != nil {
			errs = append(errs, err)
		}
		<-rt.apiDone
	}
	if rt.api != nil {
		rt.api.Stop()
	}
	if rt.producer != nil {
		rt.producer.Stop()
	}
	if rt.network != nil {
		rt.network.Stop()
	}
	if rt.service != nil {
		rt.service.Stop()
	}
	if rt.public != nil {
		if err := rt.public.Stop(); err != nil {
			errs = append(errs, err)
		}
		if err := rt.public.Host().Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if rt.client != nil {
		rt.client.Close()
	}
	if rt.engine != nil {
		if err := rt.engine.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if n.db != nil {
		n.db.Close()
	}
	n.closeDataDir()
	close(n.shutDown)
	return errors.Join(errs...)
}
