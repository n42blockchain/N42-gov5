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
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/gofrs/flock"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/node"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
	"github.com/n42blockchain/N42/internal/txindexer"
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

func run(ctx context.Context, args []string) (runErr error) {
	flags := flag.NewFlagSet("n42-execution-service", flag.ContinueOnError)
	genesisFile := flags.String("genesis", "", "Absolute explicitly provisioned performance genesis file")
	datadir := flags.String("data-dir", "", "Already initialized dedicated performance execution datadir")
	capacity := flags.Int("capacity", 64, "Bounded import-once registry capacity (1..256)")
	profileDir := flags.String("profile-dir", "", "Optional fresh private CPU/block/mutex profile output directory; default disabled, no listener")
	parallelEVM := flags.Bool("parallel-evm", false, "Explicitly use existing Block-STM for performance imports above four transactions")
	reuseOwnBuild := flags.Bool("reuse-own-build", false, "Reuse one matching owner-local candidate execution after public sealed-header validation (live root replay remains)")
	txIndexTail := flags.Bool("txindex-tail", false, "Explicitly serve transaction lookups from the restart-rebuilt tail and sealed segments; fail closed if previously enabled but now disabled")
	commitOwnerFile := flags.String("commit-owner-file", "", "Optional private 32-byte trusted finality owner capability file; default disables commit")
	ingressManifest := flags.String("strict-ingress-manifest", "", "Explicit finalized performance corpus manifest; enables private strict ingress and owner queue")
	resumePlan := flags.String("resume-ingress-plan", "", "Explicit four-frame resume plan; mutually exclusive with strict ingress")
	resumeProof := flags.String("resume-ingress-source-proof", "", "Exact original-source mapping proof; independently reconstructed by this owner")
	resumeSource := flags.String("resume-ingress-source", "", "Original immutable signed corpus root; owner rehashes all shards")
	resumeManifest := flags.String("resume-ingress-manifest", "", "Exact finalized manifest for explicit resume ingress")
	resumePlanID := flags.String("resume-ingress-plan-id", "", "Locally pinned expected four-frame plan ID")
	boundedPlan := flags.String("bounded-ingress-plan", "", "Explicit original-source prefix plan v2; mutually exclusive with strict and four-frame ingress")
	boundedProof := flags.String("bounded-ingress-source-proof", "", "Original-source mapping proof independently reconstructed by this owner")
	boundedSource := flags.String("bounded-ingress-source", "", "Original immutable signed corpus root for bounded ingress")
	boundedManifest := flags.String("bounded-ingress-manifest", "", "Exact finalized manifest for bounded prefix ingress")
	boundedPlanID := flags.String("bounded-ingress-plan-id", "", "Locally pinned expected bounded prefix plan ID")
	ingressMaxTx := flags.Int("ingress-max-transactions", 0, "Strict queue global credits (0 selects 2000000; maximum 2000000)")
	ingressMaxBytes := flags.Int("ingress-max-encoded-bytes", 0, "Strict queue encoded credits (0 selects 512MiB; maximum 512MiB)")
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
	capsSpecified := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "ingress-max-transactions" || f.Name == "ingress-max-encoded-bytes" {
			capsSpecified = true
		}
	})
	resumeRequested := *resumePlan != "" || *resumeProof != "" || *resumeSource != "" || *resumeManifest != "" || *resumePlanID != ""
	boundedRequested := *boundedPlan != "" || *boundedProof != "" || *boundedSource != "" || *boundedManifest != "" || *boundedPlanID != ""
	if (resumeRequested && *ingressManifest != "") || (boundedRequested && (resumeRequested || *ingressManifest != "")) {
		return errors.New("strict, four-frame resume and bounded ingress are mutually exclusive")
	}
	if *ingressManifest == "" && !resumeRequested && !boundedRequested && capsSpecified {
		return errors.New("ingress credits require explicit strict or resume ingress")
	}
	var ingressOptions *sharedexec.StrictIngressOptions
	var resumeOptions *sharedexec.ResumeIngressOptions
	var boundedOptions *sharedexec.BoundedResumeIngressOptions
	var err error
	if boundedRequested {
		boundedOptions, err = boundedResumeIngressOptions(*boundedPlan, *boundedProof, *boundedSource, *boundedManifest, *boundedPlanID, *ingressMaxTx, *ingressMaxBytes)
	} else if resumeRequested {
		resumeOptions, err = resumeIngressOptions(*resumePlan, *resumeProof, *resumeSource, *resumeManifest, *resumePlanID, *ingressMaxTx, *ingressMaxBytes)
	} else {
		ingressOptions, err = strictIngressOptions(*ingressManifest, *ingressMaxTx, *ingressMaxBytes)
	}
	if err != nil {
		return err
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
	if ingressOptions != nil {
		manifest, err := sharedexec.DecodeCorpusManifest(ingressOptions.Manifest)
		if err != nil {
			return err
		}
		expected := sharedexec.Identity{Version: 1, Namespace: genesis.Config.Performance.NetworkNamespace, ChainID: genesis.Config.ChainID.String(), GenesisHash: genesisHash}
		if manifest.Descriptor.Identity != expected {
			return errors.New("strict ingress manifest differs from actual owner genesis")
		}
	}
	if resumeOptions != nil {
		resumeOptions.GenesisRaw = raw
	}
	if boundedOptions != nil {
		boundedOptions.GenesisRaw = raw
	}
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
		metadataOnly, err := sharedvalidator.IsMetadataStore(tx)
		if err != nil {
			return err
		}
		if metadataOnly {
			return errors.New("execution owner cannot use a validator metadata store")
		}
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
	bc.SetParallelEVM(*parallelEVM)
	indexer, err := txindexer.OpenExplicit(ctx, bc, db, filepath.Join(*datadir, "txindex"), *txIndexTail)
	if err != nil {
		return err
	}
	if indexer != nil {
		bc.SetTxIndexer(indexer)
		// Server.Close drains accepted requests first; then Stop drains the
		// sealer and detaches the resolver before bc/db are closed.
		defer indexer.Stop()
	}
	var server *sharedexec.Server
	if *commitOwnerFile != "" {
		server, err = sharedexec.NewWithTrustedCommitOwner(bc, *capacity, commitOwner)
	} else {
		server, err = sharedexec.New(bc, *capacity)
	}
	if err != nil {
		return err
	}
	if *reuseOwnBuild {
		if err := server.EnableOwnBuildReuse(); err != nil {
			return err
		}
	}
	var profiles *privateOwnerProfiles
	// Registered before server.Close so all tracked workers drain before profiles
	// are stopped/flushed and the completion manifest is written.
	defer func() {
		if profileErr := profiles.Close(); profileErr != nil {
			runErr = errors.Join(runErr, profileErr)
		}
	}()
	defer server.Close()
	if ingressOptions != nil {
		if err := server.EnableStrictIngress(*ingressOptions); err != nil {
			return err
		}
	}
	if resumeOptions != nil {
		if err := server.EnableResumeIngress(ctx, *resumeOptions); err != nil {
			return err
		}
	}
	if boundedOptions != nil {
		if err := server.EnableBoundedResumeIngress(ctx, *boundedOptions); err != nil {
			return err
		}
	}
	profiles, err = startPrivateOwnerProfiles(*profileDir)
	if err != nil {
		return err
	}
	socketDir := filepath.Join(*datadir, "execution-rpc")
	if err := os.Mkdir(socketDir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	strict := ingressOptions != nil || resumeOptions != nil || boundedOptions != nil
	if indexer == nil {
		return server.ServePrivateUnix(ctx, socketDir, strict)
	}
	ownerCtx, stopOwner := context.WithCancel(ctx)
	defer stopOwner()
	served := make(chan error, 1)
	go func() { served <- server.ServePrivateUnix(ownerCtx, socketDir, strict) }()
	select {
	case err := <-indexer.FatalSeal():
		// A failed seal has an ambiguous durable prefix. Keep the tail alive
		// until accepted work drains, then stop this owner for recovery.
		stopOwner()
		<-served
		return fmt.Errorf("transaction lookup tail publication failed; owner stopped: %w", err)
	case err := <-served:
		return err
	}
}
