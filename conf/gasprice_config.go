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
//
// Gas Price Oracle configuration.
// Declares GpoConfig (Blocks window, Percentile, MaxHeader/BlockHistory,
// Default / MaxPrice / IgnorePrice) and DefaultMaxPrice /
// DefaultIgnorePrice constants plus the FullNodeGPO preset used by
// eth_gasPrice and eth_feeHistory to suggest tips from recent blocks.

package conf

import (
	"math/big"

	"github.com/n42blockchain/N42/params"
)

var (
	DefaultMaxPrice    = big.NewInt(500 * params.GWei)
	DefaultIgnorePrice = big.NewInt(2 * params.Wei)
)

type GpoConfig struct {
	Blocks           int      `json:"blocks" yaml:"blocks"`
	Percentile       int      `json:"percentile" yaml:"percentile"`
	MaxHeaderHistory int      `json:"maxHeaderHistory" yaml:"maxHeaderHistory"`
	MaxBlockHistory  int      `json:"maxBlockHistory" yaml:"maxBlockHistory"`
	Default          *big.Int `json:"default,omitempty" yaml:"default,omitempty"`
	MaxPrice         *big.Int `json:"maxPrice,omitempty" yaml:"maxPrice,omitempty"`
	IgnorePrice      *big.Int `json:"ignorePrice,omitempty" yaml:"ignorePrice,omitempty"`
}

var FullNodeGPO = GpoConfig{
	Blocks:           20,
	Percentile:       60,
	MaxHeaderHistory: 1024,
	MaxBlockHistory:  1024,
	MaxPrice:         DefaultMaxPrice,
	IgnorePrice:      DefaultIgnorePrice,
}

var LightClientGPO = GpoConfig{
	Blocks:           2,
	Percentile:       60,
	MaxHeaderHistory: 300,
	MaxBlockHistory:  5,
	MaxPrice:         DefaultMaxPrice,
	IgnorePrice:      DefaultIgnorePrice,
}
