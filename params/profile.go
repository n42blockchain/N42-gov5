package params

import (
	"fmt"
	"strings"
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
