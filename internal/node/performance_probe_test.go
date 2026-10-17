package node

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/holiman/uint256"
	libcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

type probeBlockFetcher struct{ hotstuff.BlockFetcher }

// This fixture executes and certifies every block. Neither applied markers nor
// canonical progress are fabricated for the successful restart case.
func TestPerformanceValidatorRetainedCanonicalProbeAfterPhysicalReopen(t *testing.T) {
	ctx := context.Background()
	root, err := os.MkdirTemp("", "gov5-probe-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	rpcDir := filepath.Join(root, "execution-rpc")
	if err := os.Mkdir(rpcDir, 0700); err != nil {
		t.Fatal(err)
	}
	g := performanceGenesisFixture(t)
	g.BaseFee = uint256.NewInt(1)
	g.Alloc = conf.GenesisAlloc{types.Address{19: 99}: {Balance: "0x100000000"}}
	keys := make([]blscommon.SecretKey, 7)
	for i := range keys {
		keys[i], err = bls.RandKey()
		if err != nil {
			t.Fatal(err)
		}
		g.Config.HotStuff.Validators = append(g.Config.HotStuff.Validators, params.HotStuffValidatorConfig{Address: fmt.Sprintf("0x%040x", i+1), BLSKey: "0x" + hex.EncodeToString(keys[i].PublicKey().Marshal())})
	}
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	ownerPath := filepath.Join(root, "owner-data")
	openOwnerDB := func() kv.RwDB {
		return mdbx.NewMDBX(log.New()).Path(ownerPath).MustOpen()
	}
	ownerDB := openOwnerDB()
	var genesis *block.Block
	if err := ownerDB.Update(ctx, func(tx kv.RwTx) error {
		var err error
		genesis, _, err = (&internal.GenesisBlock{GenesisConfig: g}).Write(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	public := hotstuff.New(g.Config.HotStuff, g.Config)
	public.SetRewardFunc(apos.DoReward)
	if err := public.InitVerifierFromConfig(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = public.Close() })
	openOwner := func(db kv.RwDB) (*internal.BlockChain, *sharedexec.Server, *sharedexec.Client, func()) {
		chainI, err := internal.NewBlockChain(ctx, genesis, public, db, nil, g.Config)
		if err != nil {
			t.Fatal(err)
		}
		chain := chainI.(*internal.BlockChain)
		rc := commitment.NewQMDBRootComputer()
		if err := db.View(ctx, func(tx kv.Tx) error { rc.SetCold(tx); return rc.LoadFrom(tx) }); err != nil {
			t.Fatal(err)
		}
		rc.SetCold(nil)
		rc.EnableUndoRecording()
		chain.SetQMDBRootComputer(rc)
		server, err := sharedexec.New(chain, 8)
		if err != nil {
			t.Fatal(err)
		}
		socket := filepath.Join(rpcDir, "execution.sock")
		httpServer, listener, err := server.ListenUnix(socket)
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = httpServer.Serve(listener) }()
		client := sharedexec.NewClient(socket, server.Identity())
		var once sync.Once
		return chain, server, client, func() {
			once.Do(func() {
				client.Close()
				_ = httpServer.Close()
				server.Close()
				chain.Close()
				db.Close()
			})
		}
	}
	owner, _, client, closeOwner := openOwner(ownerDB)
	t.Cleanup(closeOwner)
	cfg := performanceNodeConfig(t, g)
	cfg.NodeCfg.DataDir = filepath.Join(root, "validator")
	cfg.NodeCfg.PerformanceRole = "validator"
	cfg.NodeCfg.PerformanceExecutionSocket = filepath.Join(rpcDir, "execution.sock")
	cfg.NodeCfg.Miner = false
	address := types.HexToAddress(g.Config.HotStuff.Validators[1].Address)
	cfg.Miner.Etherbase = address.Hex()
	keydir := filepath.Join(cfg.NodeCfg.DataDir, "keystore")
	if err := os.MkdirAll(keydir, 0700); err != nil {
		t.Fatal(err)
	}
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
	metadataDB, err := OpenDatabase(ctx, cfg, nil, kv.ChainDB.String())
	if err != nil {
		t.Fatal(err)
	}
	var metadataClosed sync.Once
	closeMetadata := func() { metadataClosed.Do(metadataDB.Close) }
	t.Cleanup(closeMetadata)
	leader := hotstuff.New(g.Config.HotStuff, g.Config)
	leader.Authorize(address, keys[1])
	if err := leader.InitEngineFromConfig(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = leader.Close() })
	replica, err := sharedvalidator.New(ctx, metadataDB, g.Config, genesis, leader, client)
	if err != nil {
		t.Fatal(err)
	}
	service := hotstuff.NewService(leader, nil, metadataDB, "unused", "unused")
	var serviceStopped sync.Once
	stopService := func() { serviceStopped.Do(service.Stop) }
	t.Cleanup(stopService)
	if err := replica.AttachService(service, probeBlockFetcher{}); err != nil {
		t.Fatal(err)
	}
	producer, err := sharedvalidator.NewProducer(ctx, replica, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(producer.Stop)
	lastQC := hotstuff.GenesisQC()
	for height := uint64(1); height <= 257; height++ {
		// Seven-view steps retain the same independently keyed leader. Each
		// parent certificate is nevertheless an actual public 5-of-7 CommitQC.
		view := hotstuff.ViewNumber(1 + 7*(height-1))
		leader.Engine().RestoreState(view, lastQC, lastQC, 0)
		b, err := producer.Produce(ctx, replica.CurrentBlock().Hash())
		if err != nil {
			t.Fatalf("build/import height %d: %v", height, err)
		}
		qc := &hotstuff.QuorumCertificate{View: view, BlockHash: b.Hash(), Signers: make([]bool, 7)}
		var signatures []blscommon.Signature
		for i := 0; i < 5; i++ {
			qc.Signers[i] = true
			signatures = append(signatures, keys[i].Sign(hotstuff.CommitSigningMessage(view, b.Hash())))
		}
		qc.AggregateSignature = bls.AggregateSignatures(signatures).Marshal()
		if err := replica.CommitCertificate(ctx, qc); err != nil {
			t.Fatalf("public canonical QC height %d: %v", height, err)
		}
		lastQC = *qc
		for draining := true; draining; {
			select {
			case <-leader.OutputCh():
			default:
				draining = false
			}
		}
	}
	checkpoint, err := replica.CanonicalProgress(ctx)
	if err != nil || checkpoint.Number != 257 || checkpoint.BlockHash != owner.CurrentBlock().Hash() {
		t.Fatalf("real retained checkpoint: %+v %v", checkpoint, err)
	}
	producer.Stop()
	stopService()
	_ = leader.Close()
	closeMetadata()
	closeOwner()
	// Both actual environments are closed before this fresh owner/constructor.
	owner, server, client, closeOwner := openOwner(openOwnerDB())
	defer closeOwner()
	if owner.CurrentBlock().Hash() != checkpoint.BlockHash || owner.CurrentBlock().Number64().Uint64() != 257 {
		t.Fatal("owner did not physically retain canonical checkpoint")
	}
	if _, err := client.AppliedChild(ctx, sharedexec.AppliedChildRequest{Identity: client.Identity(), ParentHash: genesis.Hash(), Number: 1, View: hotstuff.ViewNumber(math.MaxUint64)}); err == nil {
		t.Fatal("old genesis probe no longer demonstrates the bounded-window failure")
	}
	if err := PersistDataDirNetworkBinding(cfg, g.Config, genesis.Hash()); err != nil {
		t.Fatal(err)
	}
	profile, _ := params.ResolveExecutionProfile("n42")
	selection, err := resolveConfiguredGenesis(cfg, profile)
	if err != nil {
		t.Fatal(err)
	}
	construct := func() (*Node, error) {
		ctx, cancel := context.WithCancel(context.Background())
		n, err := newPerformanceValidatorNode(ctx, cancel, nodeTNewContext(), cfg, profile, selection, root)
		if err != nil {
			cancel()
		}
		return n, err
	}
	n, err := construct()
	if err != nil {
		t.Fatal("retained runtime could not restart beyond genesis window:", err)
	}
	if n.blockChain != nil || n.miner != nil || n.performanceValidator.replica.CurrentBlock().Hash() != checkpoint.BlockHash {
		t.Fatal("restart constructed local execution or replaced the checkpoint")
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	if server.ImportCalls() != 0 || server.BuildCalls() != 0 {
		t.Fatal("startup probe executed state")
	}
	// Keep body, progress and canonical/hash-number mappings intact while only
	// damaging the local header row. Owner lineage alone must not mask this.
	for _, name := range []string{"missing local header", "invalid header encoding", "incomplete header fields", "local header root changed", "local header number changed"} {
		t.Run(name, func(t *testing.T) {
			db, err := OpenDatabase(ctx, cfg, nil, kv.ChainDB.String())
			if err != nil {
				t.Fatal(err)
			}
			key := modules.HeaderKey(checkpoint.Number, checkpoint.BlockHash)
			var original []byte
			err = db.Update(ctx, func(tx kv.RwTx) error {
				raw, err := tx.GetOne(modules.Headers, key)
				if err != nil {
					return err
				}
				original = append([]byte(nil), raw...)
				if len(original) == 0 {
					return fmt.Errorf("valid fixture header missing")
				}
				switch name {
				case "missing local header":
					return tx.Delete(modules.Headers, key)
				case "invalid header encoding":
					return tx.Put(modules.Headers, key, []byte{0xff})
				case "incomplete header fields":
					return tx.Put(modules.Headers, key, []byte{0x0a, 0x00})
				default:
					header := block.CopyHeader(owner.CurrentBlock().Header().(*block.Header))
					if name == "local header root changed" {
						header.Root[0] ^= 1
					} else {
						header.Number = uint256.NewInt(checkpoint.Number + 1)
					}
					raw, err := header.Marshal()
					if err != nil {
						return err
					}
					return tx.Put(modules.Headers, key, raw)
				}
			})
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			if rejected, err := construct(); err == nil || rejected != nil {
				if rejected != nil {
					_ = rejected.Close()
				}
				t.Fatalf("damaged local header entered runtime: %v", err)
			}
			db, err = OpenDatabase(ctx, cfg, nil, kv.ChainDB.String())
			if err != nil {
				t.Fatal(err)
			}
			err = db.Update(ctx, func(tx kv.RwTx) error { return tx.Put(modules.Headers, key, original) })
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			restored, err := construct()
			if err != nil {
				t.Fatal("valid local header restoration did not restore startup:", err)
			}
			if err := restored.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
	if server.ImportCalls() != 0 || server.BuildCalls() != 0 {
		t.Fatal("local header validation executed state")
	}
	// Deliberately corrupt or move only the local test checkpoint. These are
	// rejection inputs, not fabricated execution evidence for the success case.
	for _, tc := range []struct {
		name   string
		header *block.Header
		remove bool
	}{
		{"owner behind", func() *block.Header {
			h := block.CopyHeader(owner.CurrentBlock().Header().(*block.Header))
			h.ParentHash = checkpoint.BlockHash
			h.Number = uint256.NewInt(258)
			return h
		}(), false},
		{"same height fork", func() *block.Header {
			h := block.CopyHeader(owner.CurrentBlock().Header().(*block.Header))
			h.MixDigest[0] ^= 1
			return h
		}(), false},
		{"outside retained window", block.CopyHeader(genesis.Header().(*block.Header)), false},
		{"number overflow", func() *block.Header {
			h := block.CopyHeader(owner.CurrentBlock().Header().(*block.Header))
			h.Number = uint256.NewInt(math.MaxUint64)
			return h
		}(), false},
		{"zero progress hash", block.CopyHeader(owner.CurrentBlock().Header().(*block.Header)), false},
		{"missing progress", block.CopyHeader(owner.CurrentBlock().Header().(*block.Header)), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := OpenDatabase(ctx, cfg, nil, kv.ChainDB.String())
			if err != nil {
				t.Fatal(err)
			}
			err = db.Update(ctx, func(tx kv.RwTx) error {
				tc.header.ResetHashCache()
				b := block.NewBlock(tc.header, nil).(*block.Block)
				if err := rawdb.WriteBlock(tx, b); err != nil {
					return err
				}
				if err := rawdb.WriteCanonicalHash(tx, b.Hash(), b.Number64().Uint64()); err != nil {
					return err
				}
				rawdb.WriteHeadBlockHash(tx, b.Hash())
				key := []byte("performance-canonical-progress-v1")
				if tc.remove {
					return tx.Delete(modules.HotStuffState, key)
				}
				progress := sharedvalidator.CanonicalProgress{Identity: client.Identity(), Number: b.Number64().Uint64(), BlockHash: b.Hash()}
				if tc.name == "zero progress hash" {
					progress.BlockHash = types.Hash{}
				}
				raw, err := json.Marshal(progress)
				if err != nil {
					return err
				}
				return tx.Put(modules.HotStuffState, key, raw)
			})
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			if rejected, err := construct(); err == nil || rejected != nil {
				if rejected != nil {
					_ = rejected.Close()
				}
				t.Fatalf("invalid retained checkpoint entered runtime: %v", err)
			}
		})
	}
}
