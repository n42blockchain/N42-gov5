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

const (
	// DefaultHistoryRetention is the default number of blocks of full history
	// to keep behind HEAD (~3.5 days at 3s block time).
	DefaultHistoryRetention = 100000

	// DefaultHistoryBatchLimit is the default max DB entries deleted per expiry cycle.
	DefaultHistoryBatchLimit = 5000

	// DefaultHistoryInterval is the default number of new blocks that must
	// accumulate before running expiry.
	DefaultHistoryInterval = 1000
)

// HistoryExpiryConfig controls EIP-4444 style history expiry, which removes
// old block headers, bodies, receipts, logs, and senders from the hot database
// after they fall behind the chain head by more than Retention blocks.
type HistoryExpiryConfig struct {
	// Enable activates EIP-4444 style history expiry.
	Enable bool `json:"history_expiry" yaml:"history_expiry"`

	// Retention is the number of blocks of full history to keep behind HEAD.
	// Default: 100000 (~3.5 days at 3s block time). Set to 0 to keep forever.
	Retention uint64 `json:"history_retention" yaml:"history_retention"`

	// BatchLimit is the max DB entries deleted per expiry cycle.
	// Default: 5000.
	BatchLimit int `json:"history_batch_limit" yaml:"history_batch_limit"`

	// Interval is how many new blocks must accumulate before running expiry.
	// Default: 1000.
	Interval uint64 `json:"history_interval" yaml:"history_interval"`
}

// DefaultHistoryExpiryConfig returns a HistoryExpiryConfig with sensible defaults.
// History expiry is disabled by default.
func DefaultHistoryExpiryConfig() HistoryExpiryConfig {
	return HistoryExpiryConfig{
		Enable:     false,
		Retention:  DefaultHistoryRetention,
		BatchLimit: DefaultHistoryBatchLimit,
		Interval:   DefaultHistoryInterval,
	}
}

// IsEnabled returns true if history expiry is active and a positive retention
// window has been configured.
func (c *HistoryExpiryConfig) IsEnabled() bool {
	return c.Enable && c.Retention > 0
}

// Validate fills zero-valued fields with defaults. Call this before use.
func (c *HistoryExpiryConfig) Validate() {
	if c.Retention == 0 {
		c.Retention = DefaultHistoryRetention
	}
	if c.BatchLimit == 0 {
		c.BatchLimit = DefaultHistoryBatchLimit
	}
	if c.Interval == 0 {
		c.Interval = DefaultHistoryInterval
	}
}
