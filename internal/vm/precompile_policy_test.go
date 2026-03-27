package vm

import (
	"testing"

	"github.com/n42blockchain/N42/params"
)

func TestActiveLegacyPrecompileSetSelectsParliaIstanbulVariant(t *testing.T) {
	rules := &params.Rules{IsIstanbul: true, IsParlia: true}
	if got := activeLegacyPrecompileSet(rules); got != legacyPrecompileSetIstanbulForBSC {
		t.Fatalf("activeLegacyPrecompileSet() = %q, want %q", got, legacyPrecompileSetIstanbulForBSC)
	}
}

func TestActiveLegacyPrecompileSetPrefersNewestEnabledFork(t *testing.T) {
	tests := []struct {
		name  string
		rules *params.Rules
		want  legacyPrecompileSet
	}{
		{
			name:  "prague_over_cancun",
			rules: &params.Rules{IsPrague: true, IsCancun: true},
			want:  legacyPrecompileSetPrague,
		},
		{
			name:  "osaka_over_pectra",
			rules: &params.Rules{IsOsaka: true, IsPectra: true, IsPrague: true},
			want:  legacyPrecompileSetOsaka,
		},
		{
			name:  "fusaka_over_osaka",
			rules: &params.Rules{IsFusaka: true, IsOsaka: true},
			want:  legacyPrecompileSetFusaka,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := activeLegacyPrecompileSet(tc.rules); got != tc.want {
				t.Fatalf("activeLegacyPrecompileSet() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLegacyPrecompileContractsAndAddressesStayAligned(t *testing.T) {
	sets := []legacyPrecompileSet{
		legacyPrecompileSetHomestead,
		legacyPrecompileSetBerlin,
		legacyPrecompileSetPrague,
		legacyPrecompileSetOsaka,
	}

	for _, set := range sets {
		t.Run(string(set), func(t *testing.T) {
			contracts := legacyPrecompileContractsBySet(set)
			addresses := legacyPrecompileAddressesBySet(set)
			if len(contracts) != len(addresses) {
				t.Fatalf("len(contracts) = %d, len(addresses) = %d", len(contracts), len(addresses))
			}
			for _, addr := range addresses {
				if _, ok := contracts[addr]; !ok {
					t.Fatalf("address %s missing from contracts map for set %q", addr.Hex(), set)
				}
			}
		})
	}
}
