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

package node

import (
	"flag"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/params"
)

func profile1StartupConfig() (*conf.Config, *params.ChainConfig) {
	chain := &params.ChainConfig{N42Profile: params.N42Profile1, HotStuff: &params.HotStuffConfig{
		GasLimit: 480000000, BlockIntervalMs: 500,
	}}
	cfg := &conf.Config{P2PCfg: &conf.P2PConfig{}}
	cfg.Miner.GasCeil = 30000000
	cfg.Miner.BlockIntervalMs = 2000
	return cfg, chain
}

func TestProfile1StartupTakesMinerValuesFromGenesis(t *testing.T) {
	cfg, chain := profile1StartupConfig()
	if err := enforceProfile1Startup(nil, cfg, chain, []string{"N42_PARALLEL_WORKERS=8"}); err != nil {
		t.Fatalf("clean start refused: %v", err)
	}
	if cfg.Miner.GasCeil != 480000000 || cfg.Miner.BlockIntervalMs != 500 {
		t.Fatalf("miner gasCeil %d interval %d, want the genesis values", cfg.Miner.GasCeil, cfg.Miner.BlockIntervalMs)
	}
}

func TestProfile1StartupRefusesEnvOverrides(t *testing.T) {
	cfg, chain := profile1StartupConfig()
	err := enforceProfile1Startup(nil, cfg, chain, []string{"N42_HOTSTUFF_ROTATE_ON_TIMEOUT=1", "N42_MAX_GOSSIP_MB=64"})
	if err == nil || !strings.Contains(err.Error(), "N42_HOTSTUFF_ROTATE_ON_TIMEOUT") || !strings.Contains(err.Error(), "N42_MAX_GOSSIP_MB") {
		t.Fatalf("env overrides: %v", err)
	}
	if cfg.Miner.GasCeil != 30000000 {
		t.Fatal("refused start still rewrote the miner config")
	}
}

func TestProfile1StartupRefusesFlags(t *testing.T) {
	cfg, chain := profile1StartupConfig()
	set := flag.NewFlagSet("n42", flag.ContinueOnError)
	gasCeil := &cli.Uint64Flag{Name: "miner.gasceil", Value: 30000000}
	interval := &cli.Uint64Flag{Name: "block-interval-ms", Value: 2000}
	for _, f := range []cli.Flag{gasCeil, interval} {
		if err := f.Apply(set); err != nil {
			t.Fatal(err)
		}
	}
	if err := set.Parse([]string{"--miner.gasceil", "100", "--block-interval-ms", "100"}); err != nil {
		t.Fatal(err)
	}
	ctx := cli.NewContext(cli.NewApp(), set, nil)
	err := enforceProfile1Startup(ctx, cfg, chain, nil)
	if err == nil || !strings.Contains(err.Error(), "--miner.gasceil") || !strings.Contains(err.Error(), "--block-interval-ms") {
		t.Fatalf("flag overrides: %v", err)
	}

	cfg, chain = profile1StartupConfig()
	cfg.P2PCfg.GenesisHashOverride = "0x01"
	if err := enforceProfile1Startup(nil, cfg, chain, nil); err == nil || !strings.Contains(err.Error(), "genesis hash override") {
		t.Fatalf("config-file genesis override: %v", err)
	}
}
