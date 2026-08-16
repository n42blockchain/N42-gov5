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
// Transaction pool configuration.

package conf

import "time"

// TxPoolConfig sizes the transaction pool and sets its retention policy.
//
// The pool used to have no configuration path at all: NewTxsPool read a
// package-level default and nothing could reach it, so an operator could not
// change the pool's capacity by any means. That default holds roughly 6,000
// transactions in total, which is smaller than a single block once the gas
// ceiling is raised past about 126M -- the pool, not the chain, then sets
// throughput, and it does so invisibly.
//
// Capacity costs memory roughly in proportion to the number of transactions
// held, so raising these is a deliberate trade rather than a free win.
type TxPoolConfig struct {
	// PriceLimit is the minimum gas price for admission.
	PriceLimit uint64 `json:"price_limit" yaml:"price_limit"`
	// PriceBump is the percentage a replacement transaction must exceed the
	// one it replaces.
	PriceBump uint64 `json:"price_bump" yaml:"price_bump"`

	// AccountSlots is the number of executable transactions kept per account.
	AccountSlots uint64 `json:"account_slots" yaml:"account_slots"`
	// GlobalSlots is the total number of executable transactions kept.
	GlobalSlots uint64 `json:"global_slots" yaml:"global_slots"`
	// AccountQueue is the number of non-executable transactions kept per
	// account.
	AccountQueue uint64 `json:"account_queue" yaml:"account_queue"`
	// GlobalQueue is the total number of non-executable transactions kept.
	GlobalQueue uint64 `json:"global_queue" yaml:"global_queue"`

	// Lifetime is how long a queued transaction's account may stay silent
	// before its queued transactions are dropped.
	//
	// This is the pool's only bound on transactions that can never execute on
	// their own. A transaction queued above a nonce gap waits for the gap to
	// fill; if it never does, the global queue limit is the sole other exit
	// and it exempts locally submitted transactions entirely. Locals are also
	// persisted and reloaded at every start, so without this the set can only
	// grow. Zero disables ageing and restores that behaviour.
	Lifetime time.Duration `json:"lifetime" yaml:"lifetime"`

	// DynamicSizing scales GlobalSlots down between MinGlobalSlots and
	// GlobalSlots as heap usage approaches MemoryLimitMB.
	DynamicSizing bool `json:"dynamic_sizing" yaml:"dynamic_sizing"`
	// MinGlobalSlots is the floor for dynamic sizing.
	MinGlobalSlots uint64 `json:"min_global_slots" yaml:"min_global_slots"`
	// MemoryLimitMB is the heap size dynamic sizing measures against.
	MemoryLimitMB uint64 `json:"memory_limit_mb" yaml:"memory_limit_mb"`
}

// DefaultTxPoolConfig returns the built-in pool configuration.
func DefaultTxPoolConfig() TxPoolConfig {
	return TxPoolConfig{
		PriceLimit: 1,
		PriceBump:  10,

		AccountSlots: 16,
		GlobalSlots:  4096 + 1024,
		AccountQueue: 64,
		GlobalQueue:  1024,

		Lifetime: 3 * time.Hour,

		DynamicSizing:  false,
		MinGlobalSlots: 1024,
		MemoryLimitMB:  4096,
	}
}

// Sanitize replaces values that would leave the pool unable to function with
// the defaults, and reports what it changed. A zero Lifetime is left alone:
// disabling ageing is a legitimate choice, unlike a zero capacity.
func (c *TxPoolConfig) Sanitize() []string {
	def := DefaultTxPoolConfig()
	var fixed []string
	fix := func(dst *uint64, fallback uint64, name string) {
		if *dst == 0 {
			*dst = fallback
			fixed = append(fixed, name)
		}
	}
	fix(&c.PriceBump, def.PriceBump, "price_bump")
	fix(&c.AccountSlots, def.AccountSlots, "account_slots")
	fix(&c.GlobalSlots, def.GlobalSlots, "global_slots")
	fix(&c.AccountQueue, def.AccountQueue, "account_queue")
	fix(&c.GlobalQueue, def.GlobalQueue, "global_queue")
	fix(&c.MinGlobalSlots, def.MinGlobalSlots, "min_global_slots")
	fix(&c.MemoryLimitMB, def.MemoryLimitMB, "memory_limit_mb")
	if c.MinGlobalSlots > c.GlobalSlots {
		c.MinGlobalSlots = c.GlobalSlots
		fixed = append(fixed, "min_global_slots>global_slots")
	}
	return fixed
}
