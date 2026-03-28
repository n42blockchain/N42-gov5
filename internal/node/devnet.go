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

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	internalcore "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/params"
)

// devnetGenesisBlock creates a full-featured genesis for --dev / private mode.
// All forks through Osaka are activated at genesis so that every subsystem
// (JMT, PQ crypto, parallel execution, HotStuff-2, EIP-4844 blobs, EIP-2935
// history, EIP-7702 AA, metrics, etc.) is exercised from block 0.
func devnetGenesisBlock(cfg *conf.Config) *conf.Genesis {
	period := uint64(3)
	if cfg.ChainCfg != nil && cfg.ChainCfg.HotStuff != nil && cfg.ChainCfg.HotStuff.Period > 0 {
		period = cfg.ChainCfg.HotStuff.Period
	}

	zero := big.NewInt(0)
	chainConfig := &params.ChainConfig{
		ChainID:               big.NewInt(942),
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
		ShanghaiBlock:         zero,
		CancunBlock:           zero,
		PragueTime:            zero,
		PectraTime:            zero,
		OsakaTime:             zero,
		// N42 extensions — all active from genesis
		PQPrecompilesTime: zero,
		ContentStoreTime:  zero,
		AIInferenceTime:   zero,
		RandomnessTime:    zero,
		Consensus:         params.HotStuffConsensus,
		HotStuff: &params.HotStuffConfig{
			Period:            period,
			BaseTimeout:       60000,
			MaxTimeout:        120000,
			EpochLength:       100,
			FastPropose:       true,
			MinProposeDelayMs: 200,
		},
		BlobSchedule: &params.BlobSchedule{
			Cancun: &params.BlobConfig{
				Target:                uint64Ptr(3),
				Max:                   uint64Ptr(6),
				BaseFeeUpdateFraction: uint64Ptr(3338477),
			},
		},
	}

	alloc := make(conf.GenesisAlloc)

	// Pre-fund etherbase
	if cfg.Miner.Etherbase != "" {
		addr := types.HexToAddress(cfg.Miner.Etherbase)
		alloc[addr] = conf.GenesisAccount{
			Balance: "1000000000000000000000000000", // 1B ETH
		}
	}

	// Pre-fund 50 stress-test accounts
	for i := 0; i < 50; i++ {
		seed := fmt.Sprintf("n42-stress-test-key-%d", i)
		h := sha256.Sum256([]byte(seed))
		key, err := crypto.ToECDSA(h[:])
		if err != nil {
			continue
		}
		addr := crypto.PubkeyToAddress(key.PublicKey)
		alloc[types.Address(addr)] = conf.GenesisAccount{
			Balance: "1000000000000000000000", // 1000 ETH
		}
	}

	// Deploy Prague system contracts (EIP-7002, EIP-7251, EIP-2935)
	for _, deployment := range internalcore.PragueSystemContractDeployments() {
		alloc[deployment.Address] = deployment.Account
	}

	// Deploy beacon roots contract (EIP-4788)
	alloc[params.BeaconRootsAddress] = conf.GenesisAccount{
		Balance: "0x0",
		Nonce:   1,
		Code:    types.Hex2Bytes("3373fffffffffffffffffffffffffffffffffffffffe14604d57602036146024575f5ffd5b5f35801560495762001fff810690815414603c575f5ffd5b62001fff01545f5260205ff35b5f5ffd5b62001fff42064281555f359062001fff015500"),
	}

	miners := []string{}
	if cfg.Miner.Etherbase != "" {
		miners = append(miners, cfg.Miner.Etherbase)
	}

	return &conf.Genesis{
		Config:   chainConfig,
		Alloc:    alloc,
		Miners:   miners,
		GasLimit: 30_000_000,
	}
}

func uint64Ptr(v uint64) *uint64 { return &v }
