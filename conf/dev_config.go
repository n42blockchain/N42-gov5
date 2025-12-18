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

package conf

import "time"

// DevConfig holds development and testing configuration.
type DevConfig struct {
	// TxGen enables automatic transaction generation for testing
	TxGenEnabled bool `json:"tx_gen_enabled" yaml:"tx_gen_enabled"`
	
	// TxGenMaxPerBlock is the maximum number of transactions to generate per block (0-31)
	TxGenMaxPerBlock int `json:"tx_gen_max_per_block" yaml:"tx_gen_max_per_block"`
	
	// TxGenInterval is the interval between transaction generation batches
	TxGenInterval time.Duration `json:"tx_gen_interval" yaml:"tx_gen_interval"`
	
	// TxGenGasPrice is the gas price for generated transactions (in wei)
	TxGenGasPrice uint64 `json:"tx_gen_gas_price" yaml:"tx_gen_gas_price"`
}

// DefaultDevConfig returns the default development configuration.
func DefaultDevConfig() DevConfig {
	return DevConfig{
		TxGenEnabled:     false,
		TxGenMaxPerBlock: 10,
		TxGenInterval:    time.Second,
		TxGenGasPrice:    1000000000, // 1 Gwei
	}
}

