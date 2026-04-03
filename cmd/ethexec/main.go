// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// ethexec re-executes Ethereum mainnet blocks from Geth ancient data
// to produce PlainState, changesets, and receipts.
//
// Usage:
//
//	ethexec --ancient <geth-ancient-dir> --datadir <output-dir> [--genesis <genesis.json>] [--start N] [--end N]

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
	"github.com/urfave/cli/v2"
)

func main() {
	app := &cli.App{
		Name:  "ethexec",
		Usage: "Re-execute Ethereum mainnet blocks from Geth ancient data",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "ancient",
				Usage:    "Path to Geth ancient chain directory",
				Required: true,
			},
			&cli.StringFlag{
				Name:     "datadir",
				Usage:    "Path to output MDBX database",
				Required: true,
			},
			&cli.StringFlag{
				Name:  "genesis",
				Usage: "Path to Ethereum genesis.json (for initial state)",
			},
			&cli.Uint64Flag{
				Name:  "start",
				Usage: "Start block number",
				Value: 0,
			},
			&cli.Uint64Flag{
				Name:  "end",
				Usage: "End block number (0 = all available)",
				Value: 0,
			},
			&cli.Uint64Flag{
				Name:  "commit",
				Usage: "Commit interval (blocks)",
				Value: 10000,
			},
			&cli.Uint64Flag{
				Name:  "verify",
				Usage: "State root verification interval (0=disabled)",
				Value: 0,
			},
			&cli.BoolFlag{
				Name:  "skip-errors",
				Usage: "Log gas mismatches but continue execution",
			},
			&cli.BoolFlag{
				Name:  "no-indices",
				Usage: "Skip writing TxLookup and LogIndex (faster sync)",
			},
			&cli.BoolFlag{
				Name:  "no-history",
				Usage: "Skip writing AccountsHistory/StorageHistory bitmaps (faster sync)",
			},
			&cli.BoolFlag{
				Name:  "no-outputs",
				Usage: "Skip writing output freezer (receipts, senders, witness, etc.)",
			},
		},
		Action: run,
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(c *cli.Context) error {
	ancientPath := c.String("ancient")
	datadir := c.String("datadir")
	genesisPath := c.String("genesis")
	startBlock := c.Uint64("start")
	endBlock := c.Uint64("end")
	commitInterval := c.Uint64("commit")
	verifyInterval := c.Uint64("verify")
	skipErrors := c.Bool("skip-errors")
	noIndices := c.Bool("no-indices")
	noHistory := c.Bool("no-history")
	noOutputs := c.Bool("no-outputs")

	// Open Geth ancient freezer.
	log.Info("Opening Geth ancient data", "path", ancientPath)
	f, err := freezer.New(ancientPath, 0)
	if err != nil {
		return fmt.Errorf("open freezer: %w", err)
	}
	defer f.Close()
	log.Info("Freezer opened", "frozen", f.Frozen())

	// Open MDBX.
	log.Info("Opening MDBX database", "path", datadir)
	if err := os.MkdirAll(datadir, 0755); err != nil {
		return err
	}
	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).Path(datadir).Open(context.Background())
	if err != nil {
		return fmt.Errorf("open mdbx: %w", err)
	}
	defer db.Close()

	// Load genesis state if provided.
	if genesisPath != "" {
		log.Info("Loading Ethereum genesis state", "path", genesisPath)
		tx, err := db.BeginRw(context.Background())
		if err != nil {
			return err
		}
		count, err := ethel.InitEthGenesisState(tx, genesisPath)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("init genesis: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit genesis: %w", err)
		}
		log.Info("Genesis state loaded", "accounts", count)
	}

	// Open output freezer for receipts, senders, changesets.
	outAncientPath := filepath.Join(datadir, "ancient")
	outFreezer, err := freezer.New(outAncientPath, 0)
	if err != nil {
		return fmt.Errorf("open output freezer: %w", err)
	}
	defer outFreezer.Close()
	log.Info("Output freezer opened", "path", outAncientPath)

	// Set up executor.
	chainCfg := params.EthereumMainnetChainConfig
	engine := ethel.NewEthReplayEngine(chainCfg)

	cfg := ethel.ExecutorConfig{
		StartBlock:     startBlock,
		EndBlock:       endBlock,
		CommitInterval: commitInterval,
		VerifyInterval: verifyInterval,
		SkipErrors:     skipErrors,
		NoIndices:      noIndices,
		NoHistory:      noHistory,
		NoOutputs:      noOutputs,
	}

	executor := ethel.NewExecutor(f, db, chainCfg, engine, cfg, outFreezer)

	// Run with graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Info("Received shutdown signal, finishing current block...")
		cancel()
		// Second signal = force exit.
		<-sig
		log.Info("Force exit")
		os.Exit(1)
	}()

	return executor.Run(ctx)
}
