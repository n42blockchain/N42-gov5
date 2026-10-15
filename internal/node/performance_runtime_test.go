package node

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
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
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

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
	root := t.TempDir()
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
	rt := n.performanceValidator
	if err := rt.producer.SubmitTransactions([]*transaction.Transaction{tx}); err != nil {
		t.Fatal(err)
	}
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
	if err := n.Close(); err != nil {
		t.Fatal(err)
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
