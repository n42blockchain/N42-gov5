// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"fmt"
	"os"

	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/node"
	"github.com/n42blockchain/N42/params"
)

var importCommand = &cli.Command{
	Name:      "import",
	Usage:     "Import one Ethereum RLP-encoded block into an eth-el datadir",
	ArgsUsage: "<block.rlp>",
	Flags: []cli.Flag{
		DataDirFlag,
		ChainFlag,
		ProfileFlag,
	},
	Action: importRLPBlock,
}

// importRLPBlock is deliberately a one-file operation. Hive's consume-rlp
// simulator mounts ordered /blocks/0001.rlp … files and invokes this command
// once per block, which keeps failure reporting tied to the offending fixture.
func importRLPBlock(cliCtx *cli.Context) error {
	path := cliCtx.Args().First()
	if path == "" {
		return fmt.Errorf("must supply an RLP block file")
	}
	profile, err := params.ResolveExecutionProfile(DefaultConfig.NodeCfg.Profile)
	if err != nil {
		return err
	}
	if !profile.IsEthereumEL() {
		return fmt.Errorf("RLP import requires --profile eth")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read RLP block %q: %w", path, err)
	}
	blk, _, err := ethel.DecodeRawBlock(raw)
	if err != nil {
		return fmt.Errorf("decode RLP block %q: %w", path, err)
	}

	stack, err := node.NewNode(cliCtx, &DefaultConfig)
	if err != nil {
		return err
	}
	defer stack.Close()

	// RLP consume covers pre-merge Ethereum blocks too. Unlike the Engine API
	// adapter, BlockChain.InsertChain retains the full legacy validation and
	// canonical-head handling required by those fixtures.
	if _, err := stack.BlockChain().InsertChain([]block.IBlock{blk}); err != nil {
		return fmt.Errorf("import RLP block %q: %w", path, err)
	}
	return nil
}
