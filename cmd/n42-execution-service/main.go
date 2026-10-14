// n42-execution-service owns one Go execution database. It does not start a
// miner, P2P, voting service, or load validator signing keys.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gofrs/flock"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/node"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("n42-execution-service", flag.ContinueOnError)
	genesisFile := flags.String("genesis", "", "Absolute explicitly provisioned performance genesis file")
	datadir := flags.String("data-dir", "", "Already initialized dedicated performance execution datadir")
	capacity := flags.Int("capacity", 64, "Bounded import-once registry capacity (1..256)")
	commitOwnerFile := flags.String("commit-owner-file", "", "Optional private 32-byte trusted finality owner capability file; default disables commit")
	if err := flags.Parse(args); err != nil {
		return err
	}
	for _, entry := range os.Environ() {
		key := strings.SplitN(entry, "=", 2)[0]
		if strings.HasPrefix(key, "N42_") || strings.HasPrefix(key, "QS_") || strings.HasPrefix(key, "HIVE_") {
			return fmt.Errorf("standalone execution requires explicit configuration; remove inherited %s", key)
		}
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*genesisFile) || !filepath.IsAbs(*datadir) || *capacity < 1 || *capacity > 256 {
		return errors.New("absolute --genesis/--data-dir and capacity 1..256 required")
	}
	var commitOwner [32]byte
	if *commitOwnerFile != "" {
		var err error
		commitOwner, err = loadCommitOwner(*commitOwnerFile)
		if err != nil {
			return err
		}
	}
	canonicalDir, err := filepath.EvalSymlinks(*datadir)
	if err != nil || canonicalDir != *datadir || !strings.HasPrefix(canonicalDir, "/data/blockchain/gov5-performance-5m/runtime/") {
		return errors.New("execution datadir must exist without symlinks in the dedicated performance runtime")
	}
	raw, err := os.ReadFile(*genesisFile)
	if err != nil {
		return err
	}
	var genesis conf.Genesis
	if err := json.Unmarshal(raw, &genesis); err != nil {
		return err
	}
	cfg := &conf.Config{NodeCfg: conf.NodeConfig{Chain: "private", Profile: "n42", DataDir: *datadir, PerformanceGenesis: *genesisFile}, ChainCfg: genesis.Config}
	if err := node.ValidatePerformanceGenesis(cfg, &genesis); err != nil {
		return err
	}
	if genesis.Config.Performance == nil || genesis.Config.DeferredExecutionTime != nil {
		return errors.New("explicit independent performance genesis required; deferred backend is not wired")
	}
	if pool := genesis.Config.HotStuff.CommitteePool; pool != nil && pool.Enabled {
		return errors.New("shared executor committee-evidence adapter is not wired")
	}
	if genesis.Config.HotStuff.InteropV4 {
		return errors.New("shared executor H2-v4 domain adapter is not wired")
	}
	genesisBlock, _, err := (&internal.GenesisBlock{GenesisConfig: &genesis}).ToBlock()
	if err != nil {
		return err
	}
	genesisHash := genesisBlock.Hash()
	if err := node.ValidateDataDirNetworkBinding(cfg, genesis.Config, &genesisHash); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(*datadir, "network.json")); err != nil {
		return errors.New("execution database requires explicit n42 init and its network binding")
	}
	// This initializes public QC verification only. No Authorize/Seal/Start
	// call is made, and no validator private key enters the executor process.
	engine := hotstuff.New(genesis.Config.HotStuff, genesis.Config)
	defer engine.Close()
	engine.SetRewardFunc(apos.DoReward)
	if err := engine.InitVerifierFromConfig(); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(*datadir, "LOCK"))
	locked, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !locked {
		return node.ErrDatadirUsed
	}
	defer lock.Unlock()
	db, err := node.OpenDatabase(ctx, cfg, nil, kv.ChainDB.String())
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.View(ctx, func(tx kv.Tx) error {
		stored, err := rawdb.ReadCanonicalHash(tx, 0)
		if err != nil {
			return err
		}
		if stored != genesisHash {
			return errors.New("stored execution genesis mismatch")
		}
		chainCfg, err := rawdb.ReadChainConfig(tx, stored)
		if err != nil {
			return err
		}
		if chainCfg == nil {
			return errors.New("stored execution chain configuration missing")
		}
		if err := chainCfg.CheckCompatible(genesis.Config, 0); err != nil {
			return err
		}
		storedConfig, err := json.Marshal(chainCfg)
		if err != nil {
			return err
		}
		requestedConfig, err := json.Marshal(genesis.Config)
		if err != nil {
			return err
		}
		if !bytes.Equal(storedConfig, requestedConfig) {
			return errors.New("standalone execution configuration differs from initialized database")
		}
		return nil
	}); err != nil {
		return err
	}
	chain, err := internal.NewBlockChain(ctx, genesisBlock, engine, db, nil, genesis.Config)
	if err != nil {
		return err
	}
	bc := chain.(*internal.BlockChain)
	defer bc.Close()
	root := commitment.NewQMDBRootComputer()
	if err := db.View(ctx, func(tx kv.Tx) error { root.SetCold(tx); return root.LoadFrom(tx) }); err != nil {
		return err
	}
	root.SetCold(nil)
	root.EnableUndoRecording()
	bc.SetQMDBRootComputer(root)
	var server *sharedexec.Server
	if *commitOwnerFile != "" {
		server, err = sharedexec.NewWithTrustedCommitOwner(bc, *capacity, commitOwner)
	} else {
		server, err = sharedexec.New(bc, *capacity)
	}
	if err != nil {
		return err
	}
	defer server.Close()
	socketDir := filepath.Join(*datadir, "execution-rpc")
	if err := os.Mkdir(socketDir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	httpServer, listener, err := server.ListenUnix(filepath.Join(socketDir, "execution.sock"))
	if err != nil {
		return err
	}
	defer listener.Close()
	finished := make(chan error, 1)
	go func() { finished <- httpServer.Serve(listener) }()
	select {
	case err := <-finished:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
	return nil
}
