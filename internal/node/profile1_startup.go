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
	"fmt"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/params"
)

// profile1ForbiddenFlags are the command-line flags that set a value the
// Profile-1 genesis owns (standard P3): the block gas limit (A23), the block
// interval (A17) and the genesis hash of the fork digest (A1).
var profile1ForbiddenFlags = []string{"miner.gasceil", "block-interval-ms", "p2p.genesis-override"}

// enforceProfile1Startup refuses a Profile-1 start that carries a
// consensus-visible override in the environment or on the command line, and
// takes the miner's gas ceiling and block interval from the genesis.
func enforceProfile1Startup(cliCtx *cli.Context, cfg *conf.Config, chainConfig *params.ChainConfig, environ []string) error {
	refused := params.Profile1ForbiddenEnv(environ)
	for _, name := range profile1ForbiddenFlags {
		if cliCtx != nil && cliCtx.IsSet(name) {
			refused = append(refused, "--"+name)
		}
	}
	if cfg.P2PCfg != nil && strings.TrimSpace(cfg.P2PCfg.GenesisHashOverride) != "" &&
		(cliCtx == nil || !cliCtx.IsSet("p2p.genesis-override")) {
		refused = append(refused, "p2p genesis hash override (config file)")
	}
	if len(refused) > 0 {
		return fmt.Errorf("profile-1 chain: consensus-visible overrides are refused, the genesis is the only source (P3); unset: %s",
			strings.Join(refused, ", "))
	}
	cfg.Miner.GasCeil = chainConfig.HotStuff.GasLimit
	cfg.Miner.BlockIntervalMs = chainConfig.HotStuff.BlockIntervalMs
	return nil
}
