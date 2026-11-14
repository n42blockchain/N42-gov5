// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Configuration-derived plans: genesis, HTTP modules, auxiliary runtime and RPC exposure.

package node

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/contracts/deposit"
	"github.com/n42blockchain/N42/contracts/deposit/nftstake"
	"github.com/n42blockchain/N42/contracts/deposit/testnet"
	"github.com/n42blockchain/N42/contracts/deposit/token"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

type configuredGenesis struct {
	genesis              *conf.Genesis
	chainConfig          *params.ChainConfig
	genesisHash          *types.Hash
	isPrivate            bool
	requiresExplicitInit bool
}

type auxiliaryRuntimePlan struct {
	startMCPServer     bool
	startWeb3Gateway   bool
	startIngestServer  bool
	startAIRuntime     bool
	startDistributed   bool
	startBridgeRuntime bool
	startTxGenerator   bool
	disabledByProfile  []string
}

type rpcExposurePlan struct {
	registerBundlerAPI       bool
	registerSnapshotAPI      bool
	registerWitnessAPI       bool
	registerZKProofAPI       bool
	registerOtterscanAPI     bool
	registerHotStuffAdminAPI bool
}

func (g configuredGenesis) canonicalHash() (types.Hash, bool) {
	if g.genesisHash == nil {
		return types.Hash{}, false
	}
	return *g.genesisHash, true
}

func hasRegisteredNamespace(apis []jsonrpc.API, namespace string) bool {
	for _, api := range apis {
		if api.Namespace == namespace {
			return true
		}
	}
	return false
}

func appendModuleIfMissing(modules []string, module string) []string {
	for _, existing := range modules {
		if existing == module {
			return modules
		}
	}
	return append(modules, module)
}

func resolveHTTPModules(httpAPI string, apis []jsonrpc.API, consensusPlan consensusRuntimePlan) []string {
	modules := utils.SplitAndTrim(httpAPI)
	if consensusPlan.registerHotStuffAdminAPI && hasRegisteredNamespace(apis, "hotstuff") {
		modules = appendModuleIfMissing(modules, "hotstuff")
	}
	return modules
}

func resolveAuxiliaryRuntimePlan(cfg *conf.Config, profile params.ProfileDescriptor) auxiliaryRuntimePlan {
	var plan auxiliaryRuntimePlan
	if cfg == nil {
		return plan
	}

	addRuntime := func(name string, configured, supported bool) bool {
		if configured && !supported {
			plan.disabledByProfile = append(plan.disabledByProfile, name)
		}
		return configured && supported
	}

	aiConfigured := cfg.AICfg.Wallet.Enabled ||
		cfg.AICfg.Coord.Enabled ||
		cfg.AICfg.Governance.Enabled ||
		cfg.AICfg.Training.Enabled ||
		cfg.AICfg.Attestation.Enabled ||
		cfg.AICfg.Inference.Enabled
	distributedConfigured := cfg.AICfg.DDN.Enabled || cfg.CoprocessorCfg.Enabled ||
		cfg.MessagingCfg.Enabled ||
		cfg.StorageCfg.Enabled ||
		cfg.NotifyCfg.Enabled

	plan.startMCPServer = addRuntime("mcp", cfg.MCPCfg.Enabled, profile.SupportsMCPRuntime())
	plan.startWeb3Gateway = addRuntime("web3_gateway", cfg.Web3GatewayCfg.Enabled, profile.SupportsWeb3GatewayRuntime())
	plan.startIngestServer = addRuntime("ingest", cfg.IngestCfg.Enabled, profile.SupportsDeveloperRuntime())
	plan.startAIRuntime = addRuntime("ai", aiConfigured, profile.SupportsAIRuntime())
	plan.startDistributed = addRuntime("distributed", distributedConfigured, profile.SupportsDistributedRuntime())
	plan.startBridgeRuntime = addRuntime("bridge", cfg.BridgeCfg.Enabled, profile.SupportsBridgeRuntime())
	plan.startTxGenerator = addRuntime("tx_generator", cfg.DevCfg.TxGenEnabled, profile.SupportsDeveloperRuntime())
	return plan
}

func (p auxiliaryRuntimePlan) logProfileDisabled(profile params.ProfileDescriptor) {
	for _, runtime := range p.disabledByProfile {
		log.Warn("Auxiliary runtime disabled for execution profile", "runtime", runtime, "profile", profile.String())
	}
}

func resolveRPCExposurePlan(cfg *conf.Config, profile params.ProfileDescriptor, bundlerEnabled, hotstuffEnabled bool) rpcExposurePlan {
	plan := rpcExposurePlan{
		registerBundlerAPI:       bundlerEnabled,
		registerWitnessAPI:       true,
		registerZKProofAPI:       profile.SupportsZKProofAPI(),
		registerOtterscanAPI:     true,
		registerHotStuffAdminAPI: hotstuffEnabled,
	}
	if cfg != nil {
		plan.registerSnapshotAPI = cfg.SnapshotCfg.Enable
	}
	return plan
}

func resolveDepositContract(ctx context.Context, bc common.IBlockChain, db kv.RwDB, chainCfg *params.ChainConfig) (*deposit.Deposit, error) {
	if chainCfg == nil || chainCfg.Apos == nil {
		return nil, nil
	}

	depositContracts := make(map[types.Address]deposit.DepositContract)
	entries := []struct {
		addr     string
		name     string
		contract deposit.DepositContract
	}{
		{chainCfg.Apos.DepositContract, "DepositContract", new(token.Contract)},
		{chainCfg.Apos.DepositNFTContract, "DepositNFTContract", new(nftstake.Contract)},
		{chainCfg.Apos.DepositFUJIContract, "DepositFUJIContract", new(testnet.Contract)},
	}
	for _, e := range entries {
		if e.addr == "" {
			continue
		}
		var addr types.Address
		if !addr.DecodeString(e.addr) {
			return nil, fmt.Errorf("cannot decode %s address: %s", e.name, e.addr)
		}
		depositContracts[addr] = e.contract
	}
	return deposit.NewDeposit(ctx, bc, db, depositContracts), nil
}

func resolveConfiguredGenesis(cfg *conf.Config, profile params.ProfileDescriptor) (configuredGenesis, error) {
	chainName := strings.TrimSpace(cfg.NodeCfg.Chain)
	if !profile.SupportsConfiguredChain(chainName) {
		return configuredGenesis{}, fmt.Errorf("execution profile %q does not support chain %q", profile.String(), chainName)
	}
	if chainName == "private" {
		genesis := devnetGenesisBlock(cfg)
		if profile.IsEthereumEL() {
			genesis = ethDevGenesisBlock(cfg)
		}
		return configuredGenesis{
			genesis:     genesis,
			chainConfig: genesis.Config,
			isPrivate:   true,
		}, nil
	}

	genesisHash := params.GenesisHashByChainName(chainName)
	if genesisHash == nil {
		return configuredGenesis{}, fmt.Errorf("unknown chain: %s", chainName)
	}
	chainConfig := params.ChainConfigByChainName(chainName)
	if chainConfig == nil {
		return configuredGenesis{}, fmt.Errorf("unknown chain config: %s", chainName)
	}
	genesis := internal.GenesisByChainName(chainName)

	return configuredGenesis{
		genesis:              genesis,
		chainConfig:          chainConfig,
		genesisHash:          genesisHash,
		requiresExplicitInit: genesis == nil,
	}, nil
}

func isPublicEthereumChain(chain string) bool {
	switch strings.TrimSpace(chain) {
	case "eth-mainnet", "eth-sepolia", "eth-holesky":
		return true
	default:
		return false
	}
}

func explicitInitRequiredError(chain, dataDir string) error {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		dataDir = "<datadir>"
	}
	return fmt.Errorf(
		"chain %q requires explicit genesis initialization in a dedicated datadir; run `n42 init --profile eth --chain %s --data.dir %s <genesis.json>` first",
		chain,
		chain,
		dataDir,
	)
}

func publicEthereumPresetUnavailableError(chain string) error {
	return fmt.Errorf(
		"public Ethereum preset %q is bound to canonical MPT/genesis/forks, but live Ethereum consensus/sync is not wired in this binary yet; use `--profile eth --chain private` for Hive/EEST or a different datadir with preinitialized data",
		chain,
	)
}

func bundlerChainID(chainCfg *params.ChainConfig) (uint64, error) {
	if chainCfg == nil || chainCfg.ChainID == nil {
		return 0, errors.New("bundler requires chain ID")
	}
	return chainCfg.ChainID.Uint64(), nil
}
