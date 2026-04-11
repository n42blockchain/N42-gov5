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
// Encrypted (anti-MEV) transaction pool configuration.
// EncryptedPoolCfg controls the threshold-encrypted mempool with
// MaxSize and BlockWindow bounds and exposes DefaultEncryptedPoolMaxSize
// and DefaultEncryptedPoolBlockWindow plus Min/Max bounds used by
// Validate to keep memory usage safe under adversarial load.

package conf

import "fmt"

const (
	// DefaultEncryptedPoolMaxSize is the default maximum number of encrypted
	// transactions held in the pool concurrently.
	DefaultEncryptedPoolMaxSize = 4096

	// DefaultEncryptedPoolBlockWindow is the default number of blocks ahead
	// that encrypted transactions may target.
	DefaultEncryptedPoolBlockWindow uint64 = 256

	// MinEncryptedPoolMaxSize is the minimum allowed pool size.
	MinEncryptedPoolMaxSize = 64

	// MaxEncryptedPoolMaxSize is the maximum allowed pool size to prevent
	// excessive memory usage.
	MaxEncryptedPoolMaxSize = 65536

	// MaxEncryptedPoolBlockWindow is the maximum allowed block window.
	MaxEncryptedPoolBlockWindow uint64 = 1024
)

// EncryptedPoolCfg holds configuration for the encrypted (anti-MEV) transaction pool.
type EncryptedPoolCfg struct {
	// Enabled activates the encrypted mempool for MEV protection.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// MaxSize is the maximum number of encrypted transactions the pool
	// will hold concurrently. Default: 4096.
	MaxSize int `json:"max_size" yaml:"max_size"`

	// BlockWindow controls how far ahead (in blocks) encrypted transactions
	// may target. Transactions targeting a block more than BlockWindow blocks
	// in the future are rejected. Default: 256.
	BlockWindow uint64 `json:"block_window" yaml:"block_window"`
}

// IsEnabled returns true if the encrypted pool is explicitly enabled.
func (c *EncryptedPoolCfg) IsEnabled() bool {
	return c.Enabled
}

// Validate checks that configuration values are within acceptable ranges
// and applies defaults where needed. Returns an error for invalid values.
func (c *EncryptedPoolCfg) Validate() error {
	if !c.Enabled {
		return nil
	}

	if c.MaxSize == 0 {
		c.MaxSize = DefaultEncryptedPoolMaxSize
	}
	if c.MaxSize < MinEncryptedPoolMaxSize {
		return fmt.Errorf("encrypted pool: MaxSize %d below minimum %d", c.MaxSize, MinEncryptedPoolMaxSize)
	}
	if c.MaxSize > MaxEncryptedPoolMaxSize {
		return fmt.Errorf("encrypted pool: MaxSize %d exceeds maximum %d", c.MaxSize, MaxEncryptedPoolMaxSize)
	}

	if c.BlockWindow == 0 {
		c.BlockWindow = DefaultEncryptedPoolBlockWindow
	}
	if c.BlockWindow > MaxEncryptedPoolBlockWindow {
		return fmt.Errorf("encrypted pool: BlockWindow %d exceeds maximum %d", c.BlockWindow, MaxEncryptedPoolBlockWindow)
	}

	return nil
}
