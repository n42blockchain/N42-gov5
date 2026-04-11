// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// catch_up.go — top-level catch-up entry point for eth-el.
//
// CatchUpConfig wires the input and output freezers, the MDBX database,
// the chain config, the consensus engine, and the commit interval into a
// single call. CatchUp initialises genesis state when requested, resumes
// from ReadProgress, and drives an Executor from localHead+1 up to the
// frozen tip. Once caught up, the caller switches to the engineAPI
// live loop so the consensus layer can drive NewPayload forward.

package ethel

import (
	"context"

	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

// CatchUpConfig configures the catch-up process.
type CatchUpConfig struct {
	// InputFreezer is the source of chain data (Geth ancient or BT-downloaded segments).
	InputFreezer *freezer.Freezer
	// OutputFreezer stores execution outputs (receipts, senders, changesets, etc.).
	OutputFreezer *freezer.Freezer
	// DB is the MDBX database for state.
	DB kv.RwDB
	// ChainConfig for fork rules.
	ChainConfig *params.ChainConfig
	// Engine for consensus (block rewards, etc.).
	Engine consensus.Engine
	// GenesisPath is the path to genesis.json (for initial state).
	GenesisPath string
	// CommitInterval is the MDBX commit interval.
	CommitInterval uint64
}

// CatchUp synchronizes a node from its current state to the latest available
// block in the input freezer. This is the main entry point for bootstrapping
// a new node or catching up after being offline.
//
// Flow:
//  1. Initialize genesis state if needed
//  2. Resume from last committed block
//  3. Execute all available blocks from input freezer
//  4. When caught up, caller switches to Engine API (CL-driven) mode
func CatchUp(ctx context.Context, cfg CatchUpConfig) error {
	if cfg.CommitInterval == 0 {
		cfg.CommitInterval = 10000
	}

	// 1. Initialize genesis if needed.
	if cfg.GenesisPath != "" {
		tx, err := cfg.DB.BeginRw(ctx)
		if err != nil {
			return err
		}
		count, err := InitEthGenesisState(tx, cfg.GenesisPath)
		if err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		if count > 0 {
			log.Info("Genesis state initialized", "accounts", count)
		}
	}

	// 2. Check how far behind we are.
	frozen := cfg.InputFreezer.Frozen()
	if frozen == 0 {
		log.Info("No blocks in input freezer, waiting for BT sync or CL")
		return nil
	}

	tx, err := cfg.DB.BeginRo(ctx)
	if err != nil {
		return err
	}
	localHead := ReadProgress(tx)
	tx.Rollback()

	if localHead >= frozen-1 {
		log.Info("Already caught up", "head", localHead, "frozen", frozen)
		return nil
	}

	// 3. Execute from localHead+1 to frozen-1.
	startBlock := localHead + 1
	if startBlock == 0 {
		startBlock = 1 // skip genesis block (state already loaded)
	}

	log.Info("Catching up", "from", startBlock, "to", frozen-1,
		"behind", frozen-1-startBlock)

	executor := NewExecutor(
		cfg.InputFreezer,
		cfg.DB,
		cfg.ChainConfig,
		cfg.Engine,
		ExecutorConfig{
			StartBlock:     startBlock,
			EndBlock:       frozen - 1,
			CommitInterval: cfg.CommitInterval,
		},
		cfg.OutputFreezer,
	)

	return executor.Run(ctx)
}
