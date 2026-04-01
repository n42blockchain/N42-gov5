package params

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/params/networkname"
)

func TestResolveNetworkPresetCanonicalizesAliases(t *testing.T) {
	preset, err := ResolveNetworkPreset(networkname.EthereumTestnetAlias, "eth")
	if err != nil {
		t.Fatalf("ResolveNetworkPreset returned error: %v", err)
	}
	if preset.Chain != networkname.EthereumSepoliaChainName {
		t.Fatalf("chain = %q, want %q", preset.Chain, networkname.EthereumSepoliaChainName)
	}
	if preset.Profile.Name() != ExecutionProfileEthereumEL {
		t.Fatalf("profile = %q, want %q", preset.Profile.Name(), ExecutionProfileEthereumEL)
	}
	if preset.Commitment != StateCommitmentPresetEthereumMPT {
		t.Fatalf("commitment = %q, want %q", preset.Commitment, StateCommitmentPresetEthereumMPT)
	}
	if !preset.RequiresExplicitInit {
		t.Fatal("expected public ethereum preset to require explicit init")
	}
}

func TestResolveNetworkPresetSupportsReplayVariants(t *testing.T) {
	for _, chain := range []string{"mainnet_compat", "mainnet_v2"} {
		t.Run(chain, func(t *testing.T) {
			preset, err := ResolveNetworkPreset(chain, "n42")
			if err != nil {
				t.Fatalf("ResolveNetworkPreset returned error: %v", err)
			}
			if preset.Chain != chain {
				t.Fatalf("chain = %q, want %q", preset.Chain, chain)
			}
			if preset.Commitment != StateCommitmentPresetJMT {
				t.Fatalf("commitment = %q, want %q", preset.Commitment, StateCommitmentPresetJMT)
			}
		})
	}
}

func TestInferNetworkPresetFromChainConfigRecognizesReplayVariants(t *testing.T) {
	preset, ok := InferNetworkPresetFromChainConfig(&ChainConfig{
		ChainID:       big.NewInt(94),
		Consensus:     AposConsensu,
		ShanghaiBlock: nil,
		CancunBlock:   nil,
	})
	if !ok {
		t.Fatal("expected preset inference to succeed")
	}
	if preset.Chain != "mainnet_compat" {
		t.Fatalf("chain = %q, want %q", preset.Chain, "mainnet_compat")
	}

	preset, ok = InferNetworkPresetFromChainConfig(&ChainConfig{
		ChainID:       big.NewInt(94),
		Consensus:     AposConsensu,
		ShanghaiBlock: big.NewInt(0),
		CancunBlock:   big.NewInt(0),
		BeijingBlock:  big.NewInt(0),
	})
	if !ok {
		t.Fatal("expected preset inference to succeed")
	}
	if preset.Chain != "mainnet_v2" {
		t.Fatalf("chain = %q, want %q", preset.Chain, "mainnet_v2")
	}
}
