package node

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"
	libcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

type performanceLagFetcher struct {
	hotstuff.BlockFetcher
	replica *sharedvalidator.Replica
	behind  atomic.Uint64
	gated   chan struct{}
}

func (f *performanceLagFetcher) HeightBehind() uint64 { return f.behind.Load() }
func (f *performanceLagFetcher) BlockApplied(hash types.Hash, number uint64) bool {
	return f.replica.BlockApplied(hash, number)
}
func (f *performanceLagFetcher) CatchUp() {
	select {
	case f.gated <- struct{}{}:
	default:
	}
	f.BlockFetcher.CatchUp()
}

func TestPerformanceRuntimePathsRejectAliasesAndPublicFiles(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "key")
	if err := os.WriteFile(file, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := privatePerformancePath(file, root, false, false); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(file, alias); err != nil {
		t.Fatal(err)
	}
	if err := privatePerformancePath(alias, root, false, false); err == nil {
		t.Fatal("symlink bypassed private path binding")
	}
	if err := os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	if err := privatePerformancePath(file, root, false, false); err == nil {
		t.Fatal("public key file accepted")
	}
	if err := os.Chmod(file, 0600); err != nil {
		t.Fatal(err)
	}
	if err := privatePerformancePath(file, root, false, true); err == nil {
		t.Fatal("regular file accepted as Unix socket")
	}
	if err := privatePerformancePath(filepath.Join(root, "..", "outside"), root, true, false); err == nil {
		t.Fatal("unclean outside path accepted")
	}
	socket := filepath.Join(root, "owner.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	if err := privatePerformancePath(socket, root, false, true); err != nil {
		t.Fatal(err)
	}
}

func TestPerformanceValidatorNodeRunsRemoteExecutionWithoutLocalChain(t *testing.T) {
	// Unix socket paths include the validator subdirectories and must fit
	// the platform's sockaddr_un limit; testing.T's full-name path is longer.
	root, err := os.MkdirTemp("", "gov5-node-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	g := performanceGenesisFixture(t)
	var keys []blscommon.SecretKey
	for i := 0; i < 7; i++ {
		key, err := bls.RandKey()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
		g.Config.HotStuff.Validators = append(g.Config.HotStuff.Validators, params.HotStuffValidatorConfig{Address: fmt.Sprintf("0x%040x", i+1), BLSKey: "0x" + hex.EncodeToString(key.PublicKey().Marshal())})
	}
	sender, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	to := types.HexToAddress("0x1000")
	g.Alloc = conf.GenesisAlloc{crypto.PubkeyToAddress(sender.PublicKey): {Balance: "0x1000000000000000000"}, to: {Balance: "0x1"}}
	g.BaseFee = uint256.NewInt(1)
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	ownerdb := memdb.New(t.TempDir())
	defer ownerdb.Close()
	var gb *block.Block
	if err := ownerdb.Update(context.Background(), func(tx kv.RwTx) error {
		var e error
		gb, _, e = (&internal.GenesisBlock{GenesisConfig: g}).Write(tx)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	public := hotstuff.New(g.Config.HotStuff, g.Config)
	defer public.Close()
	public.SetRewardFunc(apos.DoReward)
	if err := public.InitVerifierFromConfig(); err != nil {
		t.Fatal(err)
	}
	ownerI, err := internal.NewBlockChain(context.Background(), gb, public, ownerdb, nil, g.Config)
	if err != nil {
		t.Fatal(err)
	}
	owner := ownerI.(*internal.BlockChain)
	defer owner.Close()
	rc := commitment.NewQMDBRootComputer()
	if err := ownerdb.View(context.Background(), func(tx kv.Tx) error { rc.SetCold(tx); return rc.LoadFrom(tx) }); err != nil {
		t.Fatal(err)
	}
	rc.SetCold(nil)
	rc.EnableUndoRecording()
	owner.SetQMDBRootComputer(rc)
	executor, err := sharedexec.New(owner, 8)
	if err != nil {
		t.Fatal(err)
	}
	ownerdir := filepath.Join(root, "owner")
	if err := os.Mkdir(ownerdir, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(ownerdir, "execute.sock")
	httpServer, listener, err := executor.ListenUnix(socket)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = httpServer.Serve(listener) }()
	defer func() { httpServer.Close(); executor.Close() }()
	cfg := performanceNodeConfig(t, g)
	cfg.NodeCfg.DataDir = filepath.Join(root, "validator")
	cfg.NodeCfg.PerformanceRole = "validator"
	cfg.NodeCfg.PerformanceExecutionSocket = socket
	cfg.NodeCfg.Miner = true
	if err := os.Mkdir(cfg.NodeCfg.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	keydir := filepath.Join(cfg.NodeCfg.DataDir, "keystore")
	if err := os.Mkdir(keydir, 0700); err != nil {
		t.Fatal(err)
	}
	// View one is led by validator index one; only this node's key is written.
	address := types.HexToAddress(g.Config.HotStuff.Validators[1].Address)
	cfg.Miner.Etherbase = address.Hex()
	if err := os.WriteFile(filepath.Join(keydir, "bls_"+strings.ToLower(address.Hex())+".key"), []byte(hex.EncodeToString(keys[1].Marshal())), 0600); err != nil {
		t.Fatal(err)
	}
	pkey, _, err := libcrypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	praw, err := pkey.Raw()
	if err != nil {
		t.Fatal(err)
	}
	pfile := filepath.Join(cfg.NodeCfg.DataDir, "p2p.key")
	if err := os.WriteFile(pfile, []byte(hex.EncodeToString(praw)), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.P2PCfg = &conf.P2PConfig{PrivateKey: pfile, LocalIP: "127.0.0.1", NoDiscovery: true, MaxPeers: 10}
	db, err := OpenDatabase(context.Background(), cfg, nil, kv.ChainDB.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := sharedvalidator.InitializeMetadata(context.Background(), db, g.Config, gb); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	if err := PersistDataDirNetworkBinding(cfg, g.Config, gb.Hash()); err != nil {
		t.Fatal(err)
	}
	profile, _ := params.ResolveExecutionProfile("n42")
	genesis, err := resolveConfiguredGenesis(cfg, profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePerformanceNetworkBinding(cfg, genesis); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, tc := range []struct {
		name   string
		change func(*conf.Config)
	}{
		{"missing role", func(c *conf.Config) { c.NodeCfg.PerformanceRole = "" }},
		{"unknown role", func(c *conf.Config) { c.NodeCfg.PerformanceRole = "executor" }},
		{"execution HTTP", func(c *conf.Config) { c.NodeCfg.HTTP = true }},
		{"execution Engine API", func(c *conf.Config) { c.NodeCfg.AuthRPC = true }},
		{"missing endpoint", func(c *conf.Config) { c.NodeCfg.PerformanceExecutionSocket = "" }},
		{"wrong key identity", func(c *conf.Config) { c.Miner.Etherbase = g.Config.HotStuff.Validators[2].Address }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := *cfg
			tc.change(&copy)
			attemptCtx, attemptCancel := context.WithCancel(context.Background())
			defer attemptCancel()
			if rejected, err := newPerformanceValidatorNode(attemptCtx, attemptCancel, nodeTNewContext(), &copy, profile, genesis, root); err == nil || rejected != nil {
				if rejected != nil {
					_ = rejected.Close()
				}
				t.Fatal("invalid runtime selection entered metadata node")
			}
		})
	}
	t.Run("mismatched private key", func(t *testing.T) {
		path := filepath.Join(keydir, "bls_"+strings.ToLower(address.Hex())+".key")
		correct, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(hex.EncodeToString(keys[0].Marshal())), 0600); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.WriteFile(path, correct, 0600); err != nil {
				t.Error(err)
			}
		}()
		attemptCtx, attemptCancel := context.WithCancel(context.Background())
		defer attemptCancel()
		if rejected, err := newPerformanceValidatorNode(attemptCtx, attemptCancel, nodeTNewContext(), cfg, profile, genesis, root); err == nil || rejected != nil || !strings.Contains(err.Error(), "does not match genesis") {
			if rejected != nil {
				_ = rejected.Close()
			}
			t.Fatalf("wrong BLS private key accepted: %v", err)
		}
	})
	t.Run("inherited runtime option", func(t *testing.T) {
		t.Setenv("N42_TEST_RUNTIME_SENTINEL", "not-a-configuration")
		attemptCtx, attemptCancel := context.WithCancel(context.Background())
		defer attemptCancel()
		if rejected, err := newPerformanceValidatorNode(attemptCtx, attemptCancel, nodeTNewContext(), cfg, profile, genesis, root); err == nil || rejected != nil || !strings.Contains(err.Error(), "remove inherited N42_TEST_RUNTIME_SENTINEL") {
			if rejected != nil {
				_ = rejected.Close()
			}
			t.Fatalf("inherited runtime option accepted: %v", err)
		}
	})
	// The public entrypoint pins the real dedicated root. The temporary test
	// root is available only to this package's constructor test seam.
	if rejected, err := NewNode(nodeTNewContext(), cfg); rejected != nil || err == nil || !strings.Contains(err.Error(), "dedicated runtime root") {
		if rejected != nil {
			_ = rejected.Close()
		}
		t.Fatalf("public runtime dispatch bypassed fixed root: %v", err)
	}
	n, err := newPerformanceValidatorNode(ctx, cancel, nodeTNewContext(), cfg, profile, genesis, root)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if n.blockChain != nil || n.miner != nil || n.sync != nil || n.accman != nil || !n.IsPerformanceValidator() {
		t.Fatal("metadata role constructed local execution or wallet services")
	}
	if _, key := public.SignerKey(); key != nil {
		t.Fatal("owner acquired validator private key")
	}
	rt := n.performanceValidator
	lag := &performanceLagFetcher{BlockFetcher: rt.network, replica: rt.replica, gated: make(chan struct{}, 1)}
	lag.behind.Store(10)
	rt.service.SetBlockFetcher(lag)
	if err := rt.service.RequestPerformanceProduction(); err == nil {
		t.Fatal("production request accepted before Start installed the vote journal")
	}
	if err := n.Start(); err != nil {
		t.Fatal(err)
	}
	if err := n.Start(); err != ErrNodeRunning {
		t.Fatalf("second start: %v", err)
	}
	tx, err := transaction.SignNewTx(sender, transaction.NewLondonSigner(g.Config.ChainID), &transaction.LegacyTx{Nonce: 0, To: &to, Value: uint256.NewInt(23), Gas: 21000, GasPrice: uint256.NewInt(1)})
	if err != nil {
		t.Fatal(err)
	}
	apiSocket := filepath.Join(cfg.NodeCfg.DataDir, "validator-rpc", "validator.sock")
	apiClient := sharedvalidator.NewAPIClient(apiSocket, executor.Identity())
	defer apiClient.Close()
	status, err := apiClient.Status(ctx)
	if err != nil || status.Number != 0 || status.CommittedExecutedTransactions != 0 {
		t.Fatalf("initial runtime status: %+v %v", status, err)
	}
	wire, err := tx.EthEncoded()
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := apiClient.Submit(ctx, status.CanonicalProgress, [][]byte{wire})
	if err != nil || admitted.Received != 1 || admitted.QueuedTransactions != 1 {
		t.Fatalf("runtime wire admission: %+v %v", admitted, err)
	}
	if retried, err := apiClient.Submit(ctx, status.CanonicalProgress, [][]byte{wire}); err != nil || retried.QueuedTransactions != 1 {
		t.Fatalf("same-head admission retry duplicated queue: %+v %v", retried, err)
	}
	wrongIdentity := executor.Identity()
	wrongIdentity.ChainID = "999"
	wrongClient := sharedvalidator.NewAPIClient(apiSocket, wrongIdentity)
	if _, err := wrongClient.Status(ctx); err == nil {
		t.Fatal("runtime status accepted another chain identity")
	}
	wrongClient.Close()
	requestBytes, err := json.Marshal(map[string]any{
		"identity": executor.Identity(), "canonical_hash": status.BlockHash,
		"canonical_number": status.Number, "transactions": [][]byte{wire},
		"sender": "untrusted sender claim",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{string(requestBytes), "{} {}", `{"transactions":[""]}`} {
		recorder := httptest.NewRecorder()
		rt.api.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/transactions", strings.NewReader(invalid)))
		if recorder.Code == http.StatusOK {
			t.Fatal("invalid framing or caller sender claim was accepted")
		}
	}
	if err := rt.producer.SubmitRawTransactionsAtHead(ctx, [][]byte{[]byte("invalid ethereum wire")}, status.BlockHash, status.Number); err == nil {
		t.Fatal("malformed Ethereum wire was admitted")
	}
	select {
	case <-lag.gated:
	case <-time.After(2 * time.Second):
		t.Fatal("producer retry bypassed the Service's peer-lag gate")
	}
	if executor.BuildCalls() != 0 || executor.ImportCalls() != 0 {
		t.Fatal("behind validator executed a candidate before its Service gate opened")
	}
	lag.behind.Store(0)
	if err := rt.service.RequestPerformanceProduction(); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var hash types.Hash
		if err := ownerdb.View(ctx, func(tx kv.Tx) error {
			number, applied, ok, err := rawdb.ReadQMDBApplied(tx)
			if ok && number == 1 {
				hash = types.Hash(applied)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if hash != (types.Hash{}) && rt.replica.BlockApplied(hash, 1) {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("gated automatic producer never executed the Unix batch")
		}
	}
	if executor.BuildCalls() != 1 || executor.ImportCalls() != 1 {
		t.Fatal("automatic runtime producer did not own the first execution")
	}
	// This synchronous retry now checks WAL idempotence after the actual
	// Start/Service-triggered worker already executed the submitted batch.
	b, err := rt.producer.Produce(ctx, gb.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if !rt.replica.BlockApplied(b.Hash(), 1) || executor.BuildCalls() != 1 || executor.ImportCalls() != 1 {
		t.Fatal("Node producer did not execute through actual Unix owner")
	}
	if rt.replica.CurrentBlock().Hash() != gb.Hash() {
		t.Fatal("candidate finalized without CommitQC")
	}
	qc := &hotstuff.QuorumCertificate{View: 1, BlockHash: b.Hash(), Signers: make([]bool, 7)}
	var signatures []blscommon.Signature
	for i := 0; i < 5; i++ {
		qc.Signers[i] = true
		signatures = append(signatures, keys[i].Sign(hotstuff.CommitSigningMessage(1, b.Hash())))
	}
	qc.AggregateSignature = bls.AggregateSignatures(signatures).Marshal()
	if err := rt.service.CommitCertificate(ctx, qc); err != nil {
		t.Fatal(err)
	}
	if rt.replica.CurrentBlock().Hash() != b.Hash() || owner.CurrentBlock().Hash() != b.Hash() {
		t.Fatal("actual Node QC bridge failed canonicalization")
	}
	committed, err := apiClient.Status(ctx)
	if err != nil || committed.Number != 1 || committed.BlockHash != b.Hash() || committed.CommittedExecutedTransactions != 1 {
		t.Fatalf("runtime canonical progress: %+v %v", committed, err)
	}
	if _, err := apiClient.Submit(ctx, status.CanonicalProgress, [][]byte{wire}); err != sharedvalidator.ErrAdmissionHeadChanged {
		t.Fatalf("stale acknowledged batch was admitted after commit: %v", err)
	}
	if err := n.db.View(ctx, func(tx kv.Tx) error {
		cursor, err := tx.Cursor(modules.Account)
		if err != nil {
			return err
		}
		defer cursor.Close()
		key, _, err := cursor.First()
		if key != nil {
			t.Fatal("validator wrote execution accounts")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Wait for 100 Continue: the actual HTTP handler has started decoding and
	// is blocked on this private Unix body. Node close must cancel/drain it.
	partial, err := net.Dial("unix", apiSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer partial.Close()
	_ = partial.SetReadDeadline(time.Now().Add(time.Second))
	_, err = fmt.Fprintf(partial, "POST /v1/transactions HTTP/1.1\r\nHost: unix\r\nContent-Length: 10000\r\nExpect: 100-continue\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	continued, err := bufio.NewReader(partial).ReadString('\n')
	if err != nil || !strings.Contains(continued, "100 Continue") {
		t.Fatalf("decoder never blocked on body: %q %v", continued, err)
	}
	closed := make(chan error, 1)
	go func() { closed <- n.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Node close did not drain a blocked private API decoder")
	}
	if _, err := apiClient.Status(context.Background()); err == nil {
		t.Fatal("closed Node still served private ingress/status")
	}
	if _, err := os.Lstat(apiSocket); !os.IsNotExist(err) {
		t.Fatal("closed Node retained its Unix listener")
	}
	done := make(chan struct{})
	go func() { n.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Node Wait was not unblocked")
	}
	if err := n.Start(); err != ErrNodeStopped {
		t.Fatalf("closed node restarted: %v", err)
	}
	if err := n.Close(); err != ErrNodeStopped {
		t.Fatalf("closed node twice: %v", err)
	}
	if err := rt.service.RequestPerformanceProduction(); err == nil {
		t.Fatal("closed Service accepted production bootstrap")
	}
	// The independent execution owner remains live after validator shutdown.
	if !owner.HasAppliedBlock(b.Hash(), 1) {
		t.Fatal("validator close stopped execution owner")
	}
	// Closing before Start must also release the host, metadata DB and LOCK.
	unusedCtx, unusedCancel := context.WithCancel(context.Background())
	unused, err := newPerformanceValidatorNode(unusedCtx, unusedCancel, nodeTNewContext(), cfg, profile, genesis, root)
	if err != nil {
		unusedCancel()
		t.Fatal("reopen after shutdown:", err)
	}
	if err := unused.Close(); err != nil {
		t.Fatal("close before Start:", err)
	}
}
