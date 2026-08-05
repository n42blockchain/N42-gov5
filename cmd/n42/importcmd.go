// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"fmt"
	"os"

	"github.com/holiman/uint256"
	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/consensus/misc"
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

	if err := validateEthereumRLPHeader(blk.Header().(*block.Header), rlpImportParent(stack, blk), stack.BlockChain().Config()); err != nil {
		return fmt.Errorf("import RLP block %q: %w", path, err)
	}

	// RLP consume covers pre-merge Ethereum blocks too. Unlike the Engine API
	// adapter, BlockChain.InsertChain retains the full legacy validation and
	// canonical-head handling required by those fixtures.
	if _, err := stack.BlockChain().InsertChain([]block.IBlock{blk}); err != nil {
		return fmt.Errorf("import RLP block %q: %w", path, err)
	}
	return nil
}

// rlpImportParent returns the concrete parent header when it is available.
// InsertChain still owns unknown-ancestor handling; this helper only supplies
// the parent needed by Ethereum's header-transition rules.
func rlpImportParent(stack *node.Node, blk *block.Block) *block.Header {
	number := blk.Number64().Uint64()
	if number == 0 {
		return nil
	}
	parent := stack.BlockChain().GetHeader(blk.ParentHash(), uint256.NewInt(number-1))
	concrete, _ := parent.(*block.Header)
	return concrete
}

// validateEthereumRLPHeader applies the execution-layer header checks which
// a Hive RLP fixture expects. The eth profile deliberately uses a fake N42
// consensus engine, so these rules must run before InsertChain rather than
// relying on the engine's generic header verifier.
func validateEthereumRLPHeader(header, parent *block.Header, cfg *params.ChainConfig) error {
	if header == nil {
		return fmt.Errorf("missing block header")
	}
	if len(header.Extra) > 32 {
		return fmt.Errorf("invalid extraData: length %d exceeds 32 bytes", len(header.Extra))
	}
	if header.GasUsed > header.GasLimit {
		return fmt.Errorf("invalid gasUsed: have %d, gasLimit %d", header.GasUsed, header.GasLimit)
	}
	if header.GasLimit < params.MinGasLimit {
		return fmt.Errorf("invalid gas limit below %d", params.MinGasLimit)
	}
	if header.GasLimit > params.MaxGasLimit {
		return fmt.Errorf("invalid gas limit: have %d, max %d", header.GasLimit, params.MaxGasLimit)
	}
	if parent == nil {
		return nil
	}
	if header.Number == nil || parent.Number == nil {
		return fmt.Errorf("block number unavailable")
	}
	if header.Number.Uint64() != parent.Number.Uint64()+1 {
		return fmt.Errorf("invalid block number: have %d, want %d", header.Number.Uint64(), parent.Number.Uint64()+1)
	}
	if header.Time <= parent.Time {
		return fmt.Errorf("invalid timestamp: have %d, parent %d", header.Time, parent.Time)
	}
	if cfg != nil && cfg.IsLondon(header.Number.Uint64()) {
		if err := misc.VerifyEip1559Header(cfg, parent, header); err != nil {
			return err
		}
	} else if err := misc.VerifyGaslimit(parent.GasLimit, header.GasLimit); err != nil {
		return err
	}
	if cfg != nil && cfg.IsCancunAt(header.Number.Uint64(), header.Time) {
		return misc.VerifyEIP4844Header(parent, header, cfg)
	}
	return nil
}
