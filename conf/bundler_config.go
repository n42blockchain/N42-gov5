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

// BundlerConfig holds configuration for the ERC-4337 bundler service.
type BundlerConfig struct {
	// Enabled enables the ERC-4337 bundler and its RPC endpoints.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// MaxPoolSize is the maximum number of pending UserOperations.
	MaxPoolSize int `json:"max_pool_size" yaml:"max_pool_size"`

	// MaxBundleSize is the maximum operations per bundle transaction.
	MaxBundleSize int `json:"max_bundle_size" yaml:"max_bundle_size"`

	// BundleIntervalSec is the bundle creation interval in seconds.
	BundleIntervalSec int `json:"bundle_interval_sec" yaml:"bundle_interval_sec"`
}
