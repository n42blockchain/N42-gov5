// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// `fix-genesis` subcommand: repair replay databases.
// Writes block 0 and ChainConfig into a database that was built
// via replay tooling and therefore lacks the canonical genesis
// record, so the full node can open it without failing genesis
// hash checks. Targets the --datadir path for the selected --chain
// preset.

package main

import (
	"context"
	"fmt"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"
	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

func init() {
	rootCmd = append(rootCmd, fixGenesisCommand)
}

var fixGenesisCommand = &cli.Command{
	Name:  "fix-genesis",
	Usage: "Write genesis block 0 and ChainConfig to a replay database that lacks them",
	Description: `Replay databases may not have block 0 or ChainConfig written.
This command writes the missing genesis data so the node can start normally.`,
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "datadir", Usage: "Chain data directory", Required: true},
		&cli.StringFlag{Name: "chain", Usage: "Chain config name", Value: "mainnet"},
	},
	Action: runFixGenesis,
}

func runFixGenesis(cliCtx *cli.Context) error {
	dataDir := cliCtx.String("datadir")
	chainName := cliCtx.String("chain")
	ctx := context.Background()
	logger := log2.New()

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	db, err := mdbx.NewMDBX(logger).
		Path(dataDir + "/chaindata").
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
		MapSize(4 * datasize.TB).
		Open(ctx)
	if err != nil {
		return fmt.Errorf("open DB: %w", err)
	}
	defer db.Close()

	return db.Update(ctx, func(tx kv.RwTx) error {
		// Check if block 0 already exists.
		h, _ := rawdb.ReadCanonicalHash(tx, 0)
		if h != (types.Hash{}) {
			fmt.Printf("Block 0 already exists: %x\n", h)
			// Ensure ChainConfig and head pointers.
			cc, _ := rawdb.ReadChainConfig(tx, h)
			if cc == nil {
				genesis := internal.GenesisByChainName(chainName)
				if genesis != nil {
					rawdb.WriteChainConfig(tx, h, genesis.Config)
					fmt.Println("ChainConfig written ✓")
				}
			}
			return nil
		}

		genesis := internal.GenesisByChainName(chainName)
		if genesis == nil {
			return fmt.Errorf("unknown chain: %s", chainName)
		}

		g := &internal.GenesisBlock{GenesisConfig: genesis}
		blk, _, err := g.ToBlock()
		if err != nil {
			return fmt.Errorf("build genesis block: %w", err)
		}

		bn := blk.Number64().Uint64()
		hash := blk.Hash()
		fmt.Printf("Genesis block: number=%d hash=%x\n", bn, hash[:8])

		if err := rawdb.WriteBlock(tx, blk); err != nil {
			return fmt.Errorf("write block: %w", err)
		}
		if err := rawdb.WriteTd(tx, hash, bn, uint256.NewInt(0)); err != nil {
			return fmt.Errorf("write td: %w", err)
		}
		if err := rawdb.WriteCanonicalHash(tx, hash, bn); err != nil {
			return fmt.Errorf("write canonical hash: %w", err)
		}
		if err := rawdb.WriteReceipts(tx, bn, nil); err != nil {
			return fmt.Errorf("write receipts: %w", err)
		}
		if err := rawdb.WriteChainConfig(tx, hash, genesis.Config); err != nil {
			return fmt.Errorf("write chain config: %w", err)
		}

		// Set head pointers to the latest block (not genesis).
		c, err := tx.Cursor(modules.HeaderCanonical)
		if err != nil {
			return fmt.Errorf("cursor: %w", err)
		}
		defer c.Close()
		k, v, err := c.Last()
		if err != nil {
			return fmt.Errorf("read last canonical: %w", err)
		}
		if k != nil {
			var headHash types.Hash
			copy(headHash[:], v)
			rawdb.WriteHeadBlockHash(tx, headHash)
			rawdb.WriteHeadHeaderHash(tx, headHash)
			fmt.Printf("Head set to latest canonical block\n")
		}

		fmt.Println("Genesis block written ✓")
		fmt.Printf("Expected genesis hash: %x\n", params.MainnetGenesisHash[:8])
		fmt.Printf("Actual genesis hash:   %x\n", hash[:8])
		if hash == params.MainnetGenesisHash {
			fmt.Println("Genesis hash MATCH ✓")
		} else {
			fmt.Println("Genesis hash MISMATCH — chain config may differ from expected")
		}
		return nil
	})
}
