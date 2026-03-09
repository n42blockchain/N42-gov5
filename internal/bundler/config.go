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

package bundler

import (
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
)

// Config holds configuration for the ERC-4337 bundler service.
type Config struct {
	// Enabled enables the bundler service and RPC endpoints.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// EntryPoints is the list of supported EntryPoint contract addresses.
	// Default: [EntryPointV06, EntryPointV07]
	EntryPoints []types.Address `json:"entry_points" yaml:"entry_points"`

	// MaxPoolSize is the maximum number of UserOperations in the mempool.
	MaxPoolSize int `json:"max_pool_size" yaml:"max_pool_size"`

	// BundleInterval is the interval between bundle creation attempts.
	BundleInterval time.Duration `json:"bundle_interval" yaml:"bundle_interval"`

	// MaxBundleSize is the maximum number of UserOperations per bundle.
	MaxBundleSize int `json:"max_bundle_size" yaml:"max_bundle_size"`

	// MinBalance is the minimum balance required for a sender without a paymaster.
	MinBalance uint64 `json:"min_balance" yaml:"min_balance"`
}

// DefaultConfig returns a default bundler configuration.
func DefaultConfig() *Config {
	return &Config{
		Enabled:        false,
		EntryPoints:    []types.Address{vm.EntryPointV06, vm.EntryPointV07},
		MaxPoolSize:    4096,
		BundleInterval: 12 * time.Second,
		MaxBundleSize:  128,
	}
}

// IsSupportedEntryPoint checks if the given address is a supported EntryPoint.
func (c *Config) IsSupportedEntryPoint(addr types.Address) bool {
	for _, ep := range c.EntryPoints {
		if ep == addr {
			return true
		}
	}
	return false
}
