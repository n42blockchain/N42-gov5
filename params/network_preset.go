package params

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/n42blockchain/N42/params/networkname"
)

type StateCommitmentPreset string

const (
	StateCommitmentPresetJMT         StateCommitmentPreset = "jmt-blake3"
	StateCommitmentPresetBMT         StateCommitmentPreset = "bmt-blake3"
	StateCommitmentPresetVerkle      StateCommitmentPreset = "verkle"
	StateCommitmentPresetEthereumMPT StateCommitmentPreset = "ethereum-mpt"
	StateCommitmentPresetQMDB        StateCommitmentPreset = "qmdb"
)

type NetworkPreset struct {
	Chain                string
	Profile              ProfileDescriptor
	Commitment           StateCommitmentPreset
	RequiresExplicitInit bool
}

func (preset StateCommitmentPreset) StateScheme() string {
	return string(preset)
}

func ApplyStateCommitmentPreset(chainCfg *ChainConfig, preset StateCommitmentPreset) {
	if chainCfg == nil || chainCfg.StateScheme != "" {
		return
	}
	chainCfg.StateScheme = preset.StateScheme()
}

func ResolveNetworkPreset(chain, rawProfile string) (NetworkPreset, error) {
	normalizedChain := strings.TrimSpace(strings.ToLower(chain))
	if normalizedChain == "" {
		normalizedChain = networkname.MainnetChainName
	}
	profileHint := strings.TrimSpace(strings.ToLower(rawProfile))

	resolveExpectedProfile := func(expected ExecutionProfile) (ProfileDescriptor, error) {
		if profileHint == "" {
			return ResolveExecutionProfile(string(expected))
		}
		profile, err := ResolveExecutionProfile(profileHint)
		if err != nil {
			return ProfileDescriptor{}, err
		}
		if profile.Name() != expected {
			return ProfileDescriptor{}, fmt.Errorf("chain %q requires execution profile %q, got %q", normalizedChain, expected, profile.String())
		}
		return profile, nil
	}

	switch normalizedChain {
	case networkname.MainnetChainName, networkname.N42MainnetAlias:
		profile, err := resolveExpectedProfile(ExecutionProfileN42)
		if err != nil {
			return NetworkPreset{}, err
		}
		return NetworkPreset{
			Chain:      networkname.MainnetChainName,
			Profile:    profile,
			Commitment: StateCommitmentPresetJMT,
		}, nil
	case networkname.TestnetChainName, networkname.N42TestnetAlias:
		profile, err := resolveExpectedProfile(ExecutionProfileN42)
		if err != nil {
			return NetworkPreset{}, err
		}
		return NetworkPreset{
			Chain:      networkname.TestnetChainName,
			Profile:    profile,
			Commitment: StateCommitmentPresetJMT,
		}, nil
	case "mainnet_compat":
		profile, err := resolveExpectedProfile(ExecutionProfileN42)
		if err != nil {
			return NetworkPreset{}, err
		}
		return NetworkPreset{
			Chain:      "mainnet_compat",
			Profile:    profile,
			Commitment: StateCommitmentPresetJMT,
		}, nil
	case "mainnet_v2":
		profile, err := resolveExpectedProfile(ExecutionProfileN42)
		if err != nil {
			return NetworkPreset{}, err
		}
		return NetworkPreset{
			Chain:      "mainnet_v2",
			Profile:    profile,
			Commitment: StateCommitmentPresetJMT,
		}, nil
	case "mainnet_qmdb":
		profile, err := resolveExpectedProfile(ExecutionProfileN42)
		if err != nil {
			return NetworkPreset{}, err
		}
		return NetworkPreset{
			Chain:      "mainnet_qmdb",
			Profile:    profile,
			Commitment: StateCommitmentPresetQMDB,
		}, nil
	case "mainnet_qmdb_staggered":
		profile, err := resolveExpectedProfile(ExecutionProfileN42)
		if err != nil {
			return NetworkPreset{}, err
		}
		return NetworkPreset{
			Chain:      "mainnet_qmdb_staggered",
			Profile:    profile,
			Commitment: StateCommitmentPresetQMDB,
		}, nil
	case "qs_epoch_test":
		profile, err := resolveExpectedProfile(ExecutionProfileN42)
		if err != nil {
			return NetworkPreset{}, err
		}
		return NetworkPreset{
			Chain:      "qs_epoch_test",
			Profile:    profile,
			Commitment: StateCommitmentPresetQMDB,
		}, nil
	case networkname.EthereumMainnetChainName:
		profile, err := resolveExpectedProfile(ExecutionProfileEthereumEL)
		if err != nil {
			return NetworkPreset{}, err
		}
		return NetworkPreset{
			Chain:                networkname.EthereumMainnetChainName,
			Profile:              profile,
			Commitment:           StateCommitmentPresetEthereumMPT,
			RequiresExplicitInit: true,
		}, nil
	case networkname.EthereumSepoliaChainName, networkname.EthereumTestnetAlias:
		profile, err := resolveExpectedProfile(ExecutionProfileEthereumEL)
		if err != nil {
			return NetworkPreset{}, err
		}
		return NetworkPreset{
			Chain:                networkname.EthereumSepoliaChainName,
			Profile:              profile,
			Commitment:           StateCommitmentPresetEthereumMPT,
			RequiresExplicitInit: true,
		}, nil
	case networkname.EthereumHoleskyChainName:
		profile, err := resolveExpectedProfile(ExecutionProfileEthereumEL)
		if err != nil {
			return NetworkPreset{}, err
		}
		return NetworkPreset{
			Chain:                networkname.EthereumHoleskyChainName,
			Profile:              profile,
			Commitment:           StateCommitmentPresetEthereumMPT,
			RequiresExplicitInit: true,
		}, nil
	case "private":
		var profile ProfileDescriptor
		var err error
		if profileHint == "" {
			profile, err = ResolveExecutionProfile(string(ExecutionProfileN42))
		} else {
			profile, err = ResolveExecutionProfile(profileHint)
		}
		if err != nil {
			return NetworkPreset{}, err
		}
		// N42 native custom chains use QMDB (twig-forest, Blake3) — JMT is
		// deprecated for the custom chain and eth-el (eth-el uses the Ethereum
		// MPT). QMDB is the production commitment for n42 native block
		// production, so a fresh `--chain private` node bootstraps on it.
		commitment := StateCommitmentPresetQMDB
		if profile.IsEthereumEL() {
			commitment = StateCommitmentPresetEthereumMPT
		}
		return NetworkPreset{
			Chain:      "private",
			Profile:    profile,
			Commitment: commitment,
		}, nil
	default:
		return NetworkPreset{}, fmt.Errorf("unknown chain: %s", chain)
	}
}

func InferNetworkPresetFromChainConfig(chainCfg *ChainConfig) (NetworkPreset, bool) {
	if chainCfg == nil || chainCfg.ChainID == nil {
		return NetworkPreset{}, false
	}
	switch chainCfg.ChainID.Uint64() {
	case 94:
		targetChain := networkname.MainnetChainName
		switch {
		case chainCfg.StateScheme == string(StateCommitmentPresetQMDB):
			// mainnet_qmdb reuses chainId 94 but is distinguished by the qmdb
			// state scheme (hotstuff live production over replay-converted state).
			// mainnet_qmdb_staggered is the same but with a staggered fork
			// schedule (Shanghai at a non-genesis height), so split on that.
			if chainCfg.ShanghaiBlock != nil && chainCfg.ShanghaiBlock.Sign() > 0 {
				targetChain = "mainnet_qmdb_staggered"
			} else {
				targetChain = "mainnet_qmdb"
			}
		case chainCfg.ShanghaiBlock == nil && chainCfg.CancunBlock == nil:
			targetChain = "mainnet_compat"
		case isZeroBigInt(chainCfg.ShanghaiBlock) && isZeroBigInt(chainCfg.CancunBlock) && isZeroBigInt(chainCfg.BeijingBlock):
			targetChain = "mainnet_v2"
		}
		preset, err := ResolveNetworkPreset(targetChain, string(ExecutionProfileN42))
		return preset, err == nil
	case 95:
		// qs_epoch_test: isolated HotStuff reconfiguration test chain — same
		// validators/alloc as mainnet_qmdb_staggered but chainId 95 + epochLength 20.
		preset, err := ResolveNetworkPreset("qs_epoch_test", string(ExecutionProfileN42))
		return preset, err == nil
	case 1142:
		preset, err := ResolveNetworkPreset(networkname.TestnetChainName, string(ExecutionProfileN42))
		return preset, err == nil
	case 1:
		preset, err := ResolveNetworkPreset(networkname.EthereumMainnetChainName, string(ExecutionProfileEthereumEL))
		return preset, err == nil
	case 11155111:
		preset, err := ResolveNetworkPreset(networkname.EthereumSepoliaChainName, string(ExecutionProfileEthereumEL))
		return preset, err == nil
	}
	switch chainCfg.Consensus {
	case AposConsensu, HotStuffConsensus:
		preset, err := ResolveNetworkPreset("private", string(ExecutionProfileN42))
		return preset, err == nil
	case CliqueConsensus, Faker, EtHashConsensus:
		preset, err := ResolveNetworkPreset("private", string(ExecutionProfileEthereumEL))
		return preset, err == nil
	default:
		return NetworkPreset{}, false
	}
}

func isZeroBigInt(value *big.Int) bool {
	return value != nil && value.Sign() == 0
}
