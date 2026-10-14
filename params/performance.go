package params

import "fmt"

const (
	PerformanceChainID         = 4205001
	PerformanceGenesisTime     = 1791936001
	PerformanceNamespace       = "gov5-performance-5m-v1"
	PerformanceGenesisMarker   = "gov5-performance-5m-v1"
	PerformanceModeIndependent = "independent"
	PerformanceRoleLocal       = "local"
	PerformanceTransactionsETH = "ethereum-secp256k1"
)

// PerformanceChainConfig describes the isolated performance experiment.
// Only the existing independent local executor and ETH transaction rules are
// available. Shared roles and Ed25519 must fail closed until actually wired.
type PerformanceChainConfig struct {
	Version          uint64 `json:"version" yaml:"version"`
	Mode             string `json:"mode" yaml:"mode"`
	Role             string `json:"role" yaml:"role"`
	Transactions     string `json:"transactions" yaml:"transactions"`
	NetworkNamespace string `json:"networkNamespace" yaml:"network_namespace"`
}

func PerformanceConfigsEqual(a, b *PerformanceChainConfig) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// ValidatePerformanceSelection checks the explicit chain configuration. The
// caller must also validate the actual genesis header before opening storage.
func (c *ChainConfig) ValidatePerformanceSelection(chain string, profile ProfileDescriptor) error {
	if c == nil || c.Performance == nil {
		return nil
	}
	p := c.Performance
	if chain != "private" || !profile.IsN42() {
		return fmt.Errorf("performance configuration requires private chain and n42 profile")
	}
	if c.ChainID == nil || !c.ChainID.IsInt64() || c.ChainID.Int64() != PerformanceChainID {
		return fmt.Errorf("performance configuration requires chain ID %d", PerformanceChainID)
	}
	if c.Consensus != HotStuffConsensus || c.HotStuff == nil || c.StateScheme != "qmdb" {
		return fmt.Errorf("performance configuration requires HotStuff and QMDB")
	}
	if p.Version != 1 || p.NetworkNamespace != PerformanceNamespace {
		return fmt.Errorf("invalid performance configuration version or namespace")
	}
	if p.Mode != PerformanceModeIndependent || p.Role != PerformanceRoleLocal {
		return fmt.Errorf("performance mode %q role %q is not implemented; only independent/local is available", p.Mode, p.Role)
	}
	if p.Transactions != PerformanceTransactionsETH {
		return fmt.Errorf("performance transaction scheme %q is not implemented", p.Transactions)
	}
	return nil
}
