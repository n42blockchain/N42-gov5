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
	"crypto/sha256"
	"fmt"
	"math/big"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/params"
)

// devnetGenesisBlock creates a minimal genesis for --dev / private chain mode.
// It uses Clique (APoa) consensus with a 4-second block period and pre-funds
// the configured etherbase (if any) so the node can immediately start mining.
func devnetGenesisBlock(cfg *conf.Config) *conf.Genesis {
	period := uint64(4)
	if cfg.ChainCfg != nil && cfg.ChainCfg.Clique != nil && cfg.ChainCfg.Clique.Period > 0 {
		period = cfg.ChainCfg.Clique.Period
	}

	zero := big.NewInt(0)
	chainConfig := &params.ChainConfig{
		ChainID:               big.NewInt(94),
		HomesteadBlock:        zero,
		TangerineWhistleBlock: zero,
		SpuriousDragonBlock:   zero,
		ByzantiumBlock:        zero,
		ConstantinopleBlock:   zero,
		PetersburgBlock:       zero,
		IstanbulBlock:         zero,
		MuirGlacierBlock:      zero,
		BerlinBlock:           zero,
		LondonBlock:           zero,
		ArrowGlacierBlock:     zero,
		Consensus:             params.CliqueConsensus,
		Clique: &params.CliqueConfig{
			Period: period,
			Epoch:  30000,
		},
	}

	alloc := make(conf.GenesisAlloc)
	if cfg.Miner.Etherbase != "" {
		addr := types.HexToAddress(cfg.Miner.Etherbase)
		alloc[addr] = conf.GenesisAccount{
			Balance: "1000000000000000000000000000",
		}
	}

	for i := 0; i < 50; i++ {
		seed := fmt.Sprintf("n42-stress-test-key-%d", i)
		h := sha256.Sum256([]byte(seed))
		key, err := crypto.ToECDSA(h[:])
		if err != nil {
			continue
		}
		addr := crypto.PubkeyToAddress(key.PublicKey)
		alloc[types.Address(addr)] = conf.GenesisAccount{
			Balance: "1000000000000000000000",
		}
	}

	miners := []string{}
	if cfg.Miner.Etherbase != "" {
		miners = append(miners, cfg.Miner.Etherbase)
	}

	return &conf.Genesis{
		Config: chainConfig,
		Alloc:  alloc,
		Miners: miners,
	}
}
