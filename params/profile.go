package params

import (
	"fmt"
	"strings"

	"github.com/n42blockchain/N42/params/networkname"
)

type ProfileFamily string

const (
	ProfileFamilyEthereumEL ProfileFamily = "eth-el"
	ProfileFamilyN42        ProfileFamily = "n42"
)

type ExecutionProfile string

const (
	ExecutionProfileN42        ExecutionProfile = "n42"
	ExecutionProfileEthereumEL ExecutionProfile = "eth-el"
)

type ProfileDescriptor struct {
	name   ExecutionProfile
	family ProfileFamily
}

func (p ProfileDescriptor) String() string {
	return string(p.name)
}

func (p ProfileDescriptor) Name() ExecutionProfile {
	return p.name
}

func (p ProfileDescriptor) Family() ProfileFamily {
	return p.family
}

func (p ProfileDescriptor) IsEthereumEL() bool {
	return p.family == ProfileFamilyEthereumEL
}

func (p ProfileDescriptor) IsN42() bool {
	return p.family == ProfileFamilyN42
}

func (p ProfileDescriptor) SupportsBridgeRuntime() bool {
	return p.IsN42()
}

func (p ProfileDescriptor) SupportsDistributedRuntime() bool {
	return p.IsN42()
}

func (p ProfileDescriptor) SupportsAIRuntime() bool {
	return p.IsN42()
}

func (p ProfileDescriptor) SupportsMCPRuntime() bool {
	return p.SupportsAIRuntime()
}

func (p ProfileDescriptor) SupportsWeb3GatewayRuntime() bool {
	return true
}

func (p ProfileDescriptor) SupportsDeveloperRuntime() bool {
	return true
}

func (p ProfileDescriptor) SupportsZKProofAPI() bool {
	return p.IsN42()
}

func (p ProfileDescriptor) SupportsConfiguredChain(chain string) bool {
	switch strings.TrimSpace(strings.ToLower(chain)) {
	case "private":
		return true
	case networkname.MainnetChainName, networkname.TestnetChainName, "mainnet_compat", "mainnet_v2", "mainnet_qmdb", "mainnet_qmdb_staggered":
		return p.IsN42()
	case networkname.EthereumMainnetChainName, networkname.EthereumSepoliaChainName, networkname.EthereumTestnetAlias:
		return p.IsEthereumEL()
	default:
		return false
	}
}

func (p ProfileDescriptor) SupportsConsensus(consensus ConsensusType) bool {
	switch consensus {
	case AposConsensu, HotStuffConsensus:
		return p.IsN42()
	case EtHashConsensus:
		return p.IsEthereumEL()
	case CliqueConsensus, Faker:
		return true
	default:
		return false
	}
}

func ResolveExecutionProfile(raw string) (ProfileDescriptor, error) {
	normalized := strings.TrimSpace(strings.ToLower(raw))
	if normalized == "" {
		normalized = string(ExecutionProfileN42)
	}

	switch normalized {
	case string(ExecutionProfileN42):
		return ProfileDescriptor{name: ExecutionProfileN42, family: ProfileFamilyN42}, nil
	case string(ExecutionProfileEthereumEL), "eth", "ethereum", "ethereum-el":
		return ProfileDescriptor{name: ExecutionProfileEthereumEL, family: ProfileFamilyEthereumEL}, nil
	default:
		return ProfileDescriptor{}, fmt.Errorf("unsupported execution profile %q", raw)
	}
}
