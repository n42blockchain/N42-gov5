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

package runtime

import (
	"math"
	"math/big"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// Config specifies configuration flags for running the EVM.
type Config struct {
	ChainConfig *params.ChainConfig
	Difficulty  *big.Int
	Origin      types.Address
	Coinbase    types.Address
	BlockNumber *big.Int
	Time        *big.Int
	GasLimit    uint64
	GasPrice    *uint256.Int
	Value       *uint256.Int
	Debug       bool
	EVMConfig   vm.Config
	BaseFee     *uint256.Int

	State     *state.IntraBlockState
	GetHashFn func(n uint64) types.Hash
}

// setDefaults populates zero-valued fields in cfg with sensible defaults.
func setDefaults(cfg *Config) {
	if cfg.ChainConfig == nil {
		cfg.ChainConfig = &params.ChainConfig{
			ChainID:               big.NewInt(1),
			HomesteadBlock:        new(big.Int),
			TangerineWhistleBlock: new(big.Int),
			SpuriousDragonBlock:   new(big.Int),
			ByzantiumBlock:        new(big.Int),
			ConstantinopleBlock:   new(big.Int),
			PetersburgBlock:       new(big.Int),
			IstanbulBlock:         new(big.Int),
			MuirGlacierBlock:      new(big.Int),
			BerlinBlock:           new(big.Int),
			LondonBlock:           new(big.Int),
			ArrowGlacierBlock:     new(big.Int),
			GrayGlacierBlock:      new(big.Int),
			ShanghaiBlock:         new(big.Int),
			CancunBlock:           new(big.Int),
			PragueTime:            new(big.Int),
		}
	}

	if cfg.Difficulty == nil {
		cfg.Difficulty = new(big.Int)
	}
	if cfg.Time == nil {
		cfg.Time = big.NewInt(time.Now().Unix())
	}
	if cfg.GasLimit == 0 {
		cfg.GasLimit = math.MaxUint64
	}
	if cfg.GasPrice == nil {
		cfg.GasPrice = new(uint256.Int)
	}
	if cfg.Value == nil {
		cfg.Value = new(uint256.Int)
	}
	if cfg.BlockNumber == nil {
		cfg.BlockNumber = new(big.Int)
	}
	if cfg.GetHashFn == nil {
		cfg.GetHashFn = func(n uint64) types.Hash {
			return types.BytesToHash(crypto.Keccak256([]byte(new(big.Int).SetUint64(n).String())))
		}
	}
}

// Execute executes the code using the input as call data during the execution.
// It returns the EVM's return value, the new state and an error if it failed.
//
// Execute sets up an in-memory, temporary, environment for the execution of
// the given code. It makes sure that it's restored to its original state afterwards.
func Execute(code, input []byte, cfg *Config, bn uint64) ([]byte, *state.IntraBlockState, error) {
	if cfg == nil {
		cfg = new(Config)
	}
	setDefaults(cfg)

	var (
		address = types.BytesToAddress([]byte("contract"))
		vmenv   = NewEnv(cfg)
		sender  = vm.AccountRef(cfg.Origin)
	)
	if rules := cfg.ChainConfig.Rules(vmenv.Context().BlockNumber); rules.IsBerlin {
		cfg.State.PrepareAccessList(cfg.Origin, &address, vm.ActivePrecompiles(rules), nil)
	}
	cfg.State.CreateAccount(address, true)
	cfg.State.SetCode(address, code)

	ret, _, err := vmenv.Call(
		sender,
		address,
		input,
		cfg.GasLimit,
		cfg.Value,
		false, /* bailout */
	)
	return ret, cfg.State, err
}

// Create executes the code using the EVM create method.
func Create(input []byte, cfg *Config, blockNr uint64) ([]byte, types.Address, uint64, error) {
	if cfg == nil {
		cfg = new(Config)
	}
	setDefaults(cfg)

	var (
		vmenv  = NewEnv(cfg)
		sender = vm.AccountRef(cfg.Origin)
	)
	if rules := cfg.ChainConfig.Rules(vmenv.Context().BlockNumber); rules.IsBerlin {
		cfg.State.PrepareAccessList(cfg.Origin, nil, vm.ActivePrecompiles(rules), nil)
	}

	return vmenv.Create(sender, input, cfg.GasLimit, cfg.Value)
}

// Call executes the code given by the contract's address. It will return the
// EVM's return value or an error if it failed.
//
// Call, unlike Execute, requires a config and also requires the State field to
// be set.
func Call(address types.Address, input []byte, cfg *Config) ([]byte, uint64, error) {
	setDefaults(cfg)

	vmenv := NewEnv(cfg)
	sender := cfg.State.GetOrNewStateObject(cfg.Origin)

	if rules := cfg.ChainConfig.Rules(vmenv.Context().BlockNumber); rules.IsBerlin {
		cfg.State.PrepareAccessList(cfg.Origin, &address, vm.ActivePrecompiles(rules), nil)
	}

	return vmenv.Call(
		sender,
		address,
		input,
		cfg.GasLimit,
		cfg.Value,
		false, /* bailout */
	)
}
