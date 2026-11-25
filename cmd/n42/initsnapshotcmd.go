package main

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/cmd/utils"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/node"
	"github.com/n42blockchain/N42/internal/snapshotjoin"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

var (
	snapshotBundleFlag = &cli.StringFlag{
		Name:     "bundle",
		Usage:    "profile-1 snapshot bundle directory (written by n42-reth-state-dump --bundle)",
		Required: true,
	}

	initSnapshotCommand = &cli.Command{
		Name:      "init-snapshot",
		Usage:     "Initialize an empty datadir at a profile-1 snapshot bundle's pivot block",
		ArgsUsage: "<genesisPath>",
		Action:    initSnapshot,
		Flags: []cli.Flag{
			DataDirFlag,
			ChainFlag,
			ProfileFlag,
			snapshotBundleFlag,
		},
		Description: `
The init-snapshot command writes the genesis like init, then installs the
chain's state at a committed pivot block P from an offline snapshot bundle
(gate 4a): reth init-state JSONL, header(P), the portable QMDB snapshot,
header(P+1), and a commit QC anchoring P+1. It verifies the QC against the
genesis validator set, the header chain, header(P+1).Root against the
rebuilt QMDB forest (deferred execution, k = 1) and the JSONL against the
forest's live leaves, and refuses on any mismatch. The node then starts at
head P and range-syncs the blocks above it. The datadir must be empty.`,
	}
)

func initSnapshot(cliCtx *cli.Context) error {
	genesis, _ := prepareInitGenesis(cliCtx)
	if !genesis.Config.IsProfile1() {
		utils.Fatalf("init-snapshot: the genesis is not a profile-1 chain")
	}
	bundle := cliCtx.String(snapshotBundleFlag.Name)

	chaindb, err := node.OpenDatabase(cliCtx.Context, &DefaultConfig, nil, kv.ChainDB.String())
	if err != nil {
		utils.Fatalf("Failed to open database: %v", err)
	}
	defer chaindb.Close()

	var (
		genesisBlock *block.Block
		res          *snapshotjoin.Result
	)
	if err := chaindb.Update(context.Background(), func(tx kv.RwTx) error {
		storedHash, err := rawdb.ReadCanonicalHash(tx, 0)
		if err != nil {
			return err
		}
		if storedHash != (types.Hash{}) {
			return fmt.Errorf("the datadir already holds a chain (genesis %x); init-snapshot needs an empty datadir", storedHash)
		}
		if genesisBlock, err = node.WriteGenesisBlock(tx, genesis); err != nil {
			return err
		}
		if err := node.WriteChainConfig(tx, genesisBlock.Hash(), genesis); err != nil {
			return err
		}
		res, err = snapshotjoin.Import(tx, genesis.Config, genesisBlock.Hash(), bundle, nil)
		return err
	}); err != nil {
		utils.Fatalf("init-snapshot refused, nothing written: %v", err)
	}
	if err := node.PersistDataDirNetworkBinding(&DefaultConfig, genesis.Config, genesisBlock.Hash()); err != nil {
		utils.Fatalf("Failed to persist datadir network binding: %v", err)
	}
	log.Info("Snapshot installed", "genesis", genesisBlock.Hash(), "pivot", res.Pivot, "hash", res.PivotHash,
		"postStateRoot", res.Root, "accounts", res.Accounts, "storageSlots", res.Slots, "anchorView", res.AnchorView,
		"anchorBlock", res.AnchorNumber)
	fmt.Printf("init-snapshot: genesis %s, head %d %s, post-state root %s (header(%d).Root), %d accounts, %d slots, %d live leaves, anchor QC view %d on block %d\n",
		genesisBlock.Hash().Hex(), res.Pivot, res.PivotHash.Hex(), res.Root.Hex(), res.Pivot+1, res.Accounts, res.Slots, res.LiveLeaves, res.AnchorView, res.AnchorNumber)
	return nil
}
