package main

import (
	"flag"
	"math/big"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/params"
)

func TestResolveInitNetworkPresetInfersEthereumSepolia(t *testing.T) {
	ctx := newInitTestContext(t)

	preset, err := resolveInitNetworkPreset(ctx, params.EthereumSepoliaChainConfig)
	if err != nil {
		t.Fatalf("resolveInitNetworkPreset returned error: %v", err)
	}
	if preset.Chain != "eth-sepolia" {
		t.Fatalf("chain = %q, want %q", preset.Chain, "eth-sepolia")
	}
	if preset.Profile.String() != "eth-el" {
		t.Fatalf("profile = %q, want %q", preset.Profile.String(), "eth-el")
	}
}

func TestResolveInitNetworkPresetHonorsExplicitPrivateEthereumSelection(t *testing.T) {
	ctx := newInitTestContext(t)
	if err := ctx.Set(ChainFlag.Name, "private"); err != nil {
		t.Fatalf("ctx.Set(chain) returned error: %v", err)
	}
	if err := ctx.Set(ProfileFlag.Name, "eth"); err != nil {
		t.Fatalf("ctx.Set(profile) returned error: %v", err)
	}

	preset, err := resolveInitNetworkPreset(ctx, &params.ChainConfig{
		ChainID:   big.NewInt(31337),
		Consensus: params.Faker,
	})
	if err != nil {
		t.Fatalf("resolveInitNetworkPreset returned error: %v", err)
	}
	if preset.Chain != "private" {
		t.Fatalf("chain = %q, want %q", preset.Chain, "private")
	}
	if preset.Profile.String() != "eth-el" {
		t.Fatalf("profile = %q, want %q", preset.Profile.String(), "eth-el")
	}
}

func TestValidateGenesisMatchesPresetRejectsCanonicalMismatch(t *testing.T) {
	cfg := *params.EthereumMainnetChainConfig
	cfg.ShanghaiTime = big.NewInt(1)

	err := validateGenesisMatchesPreset(&cfg, params.NetworkPreset{
		Chain:      "eth-mainnet",
		Profile:    mustResolveExecutionProfile(t, "eth"),
		Commitment: params.StateCommitmentPresetEthereumMPT,
	})
	if err == nil {
		t.Fatal("expected canonical mismatch to be rejected")
	}
}

func newInitTestContext(t *testing.T) *cli.Context {
	t.Helper()

	set := flag.NewFlagSet("init-test", flag.ContinueOnError)
	for _, cliFlag := range []cli.Flag{ChainFlag, ProfileFlag} {
		if err := cliFlag.Apply(set); err != nil {
			t.Fatalf("Apply(%T) returned error: %v", cliFlag, err)
		}
	}
	return cli.NewContext(cli.NewApp(), set, nil)
}

func mustResolveExecutionProfile(t *testing.T, raw string) params.ProfileDescriptor {
	t.Helper()

	profile, err := params.ResolveExecutionProfile(raw)
	if err != nil {
		t.Fatalf("ResolveExecutionProfile returned error: %v", err)
	}
	return profile
}
