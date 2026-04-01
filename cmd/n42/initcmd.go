package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"

	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/cmd/utils"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/node"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

var (
	initCommand = &cli.Command{
		Name:      "init",
		Usage:     "Bootstrap and initialize a new genesis block",
		ArgsUsage: "<genesisPath>",
		Action:    initGenesis,
		Flags: []cli.Flag{
			DataDirFlag,
			ChainFlag,
			ProfileFlag,
		},
		Description: `
The init command initializes a new genesis block and definition for the network.
This is a destructive action and changes the network in which you will be
participating.

It expects the genesis file as argument.`,
	}
)

// initGenesis will initialise the given JSON format genesis file and writes it as
// the zero'd block (i.e. genesis) or will fail hard if it can't succeed.
func initGenesis(cliCtx *cli.Context) error {
	var (
		err          error
		genesisBlock *block.Block
	)

	// Make sure we have a valid genesis JSON
	genesisPath := cliCtx.Args().First()
	if len(genesisPath) == 0 {
		utils.Fatalf("Must supply path to genesis JSON file")
	}

	file, err := os.Open(genesisPath)
	if err != nil {
		utils.Fatalf("Failed to read genesis file: %v", err)
	}
	defer file.Close()

	genesis := new(conf.Genesis)
	if err := json.NewDecoder(file).Decode(genesis); err != nil {
		utils.Fatalf("invalid genesis file: %v", err)
	}
	conf.ApplyHiveGenesisEnv(genesis, os.LookupEnv)
	if genesis.Config == nil {
		utils.Fatalf("invalid genesis file: missing chain config")
	}
	genesis.Config = params.NormalizeConsensus(genesis.Config)

	preset, err := resolveInitNetworkPreset(cliCtx, genesis.Config)
	if err != nil {
		utils.Fatalf("invalid init network selection: %v", err)
	}
	DefaultConfig.NodeCfg.Chain = preset.Chain
	DefaultConfig.NodeCfg.Profile = preset.Profile.String()
	DefaultConfig.NodeCfg.JMTCommitment = preset.Commitment == params.StateCommitmentPresetJMT
	DefaultConfig.ChainCfg = genesis.Config

	if err := node.ValidateDataDirNetworkBinding(&DefaultConfig, genesis.Config, nil); err != nil {
		utils.Fatalf("Failed to validate datadir network binding: %v", err)
	}

	chaindb, err := node.OpenDatabase(cliCtx.Context, &DefaultConfig, nil, kv.ChainDB.String())
	if err != nil {
		utils.Fatalf("Failed to open database: %v", err)
	}
	defer chaindb.Close()

	if err := chaindb.Update(context.Background(), func(tx kv.RwTx) error {
		storedHash, err := rawdb.ReadCanonicalHash(tx, 0)
		if err != nil {
			return err
		}

		if storedHash != (types.Hash{}) {
			return fmt.Errorf("genesis state already exists")

		}
		genesisBlock, err = node.WriteGenesisBlock(tx, genesis)
		if err != nil {
			return err
		}
		if err := node.WriteChainConfig(tx, genesisBlock.Hash(), genesis); err != nil {
			return err
		}
		return nil
	}); err != nil {
		utils.Fatalf("Failed to write genesis state to database: %v", err)
	}
	if err := node.PersistDataDirNetworkBinding(&DefaultConfig, genesis.Config, genesisBlock.Hash()); err != nil {
		utils.Fatalf("Failed to persist datadir network binding: %v", err)
	}
	log.Info("Successfully wrote genesis state", "hash", genesisBlock.Hash())
	return nil
}

func resolveInitNetworkPreset(cliCtx *cli.Context, chainCfg *params.ChainConfig) (params.NetworkPreset, error) {
	explicitChain := ""
	if cliCtx.IsSet(ChainFlag.Name) {
		explicitChain = cliCtx.String(ChainFlag.Name)
	}
	explicitProfile := ""
	if cliCtx.IsSet(ProfileFlag.Name) {
		explicitProfile = cliCtx.String(ProfileFlag.Name)
	}

	if inferred, ok := params.InferNetworkPresetFromChainConfig(chainCfg); ok {
		if explicitChain == "" {
			explicitChain = inferred.Chain
		}
		if explicitProfile == "" {
			explicitProfile = inferred.Profile.String()
		}
	}
	if explicitChain == "" {
		explicitChain = "private"
	}

	preset, err := params.ResolveNetworkPreset(explicitChain, explicitProfile)
	if err != nil {
		return params.NetworkPreset{}, err
	}
	if err := validateGenesisMatchesPreset(chainCfg, preset); err != nil {
		return params.NetworkPreset{}, err
	}
	return preset, nil
}

func validateGenesisMatchesPreset(chainCfg *params.ChainConfig, preset params.NetworkPreset) error {
	if chainCfg == nil {
		return errors.New("missing genesis chain config")
	}
	if !preset.Profile.SupportsConsensus(chainCfg.Consensus) {
		return fmt.Errorf("genesis consensus %q is incompatible with profile %q", chainCfg.Consensus, preset.Profile.String())
	}
	if preset.Chain == "private" {
		return nil
	}
	expected := params.ChainConfigByChainName(preset.Chain)
	if expected == nil {
		return fmt.Errorf("unknown preset chain config %q", preset.Chain)
	}
	if !chainConfigsMatch(expected, chainCfg) {
		return fmt.Errorf("genesis chain config does not match preset %q", preset.Chain)
	}
	return nil
}

func chainConfigsMatch(expected, actual *params.ChainConfig) bool {
	if expected == nil || actual == nil {
		return expected == actual
	}
	expectedCopy := *expected
	actualCopy := *actual
	expectedCopy.ChainName = ""
	actualCopy.ChainName = ""
	return reflect.DeepEqual(&expectedCopy, &actualCopy)
}
