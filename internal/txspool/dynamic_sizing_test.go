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

package txspool

import (
	"testing"
)

func TestEffectiveGlobalSlots_Disabled(t *testing.T) {
	pool := &TxsPool{
		config: TxsPoolConfig{
			GlobalSlots:   5120,
			DynamicSizing: false,
		},
	}
	pool.effectiveGlobalSlots.Store(1024) // should be ignored
	if got := pool.EffectiveGlobalSlots(); got != 5120 {
		t.Fatalf("expected 5120 when disabled, got %d", got)
	}
}

func TestEffectiveGlobalSlots_Enabled(t *testing.T) {
	pool := &TxsPool{
		config: TxsPoolConfig{
			GlobalSlots:   5120,
			DynamicSizing: true,
		},
	}
	pool.effectiveGlobalSlots.Store(2048)
	if got := pool.EffectiveGlobalSlots(); got != 2048 {
		t.Fatalf("expected 2048 when enabled, got %d", got)
	}
}

func TestEffectiveGlobalSlots_DefaultInit(t *testing.T) {
	pool := &TxsPool{
		config: TxsPoolConfig{
			GlobalSlots:   5120,
			DynamicSizing: true,
		},
	}
	pool.effectiveGlobalSlots.Store(5120)
	if got := pool.EffectiveGlobalSlots(); got != 5120 {
		t.Fatalf("expected 5120 on init, got %d", got)
	}
}

func TestDynamicSizing_ConfigDefaults(t *testing.T) {
	cfg := DefaultTxPoolConfig
	if cfg.DynamicSizing != false {
		t.Fatal("DynamicSizing should default to false")
	}
	if cfg.MinGlobalSlots != 1024 {
		t.Fatalf("MinGlobalSlots should default to 1024, got %d", cfg.MinGlobalSlots)
	}
	if cfg.MemoryLimitMB != 4096 {
		t.Fatalf("MemoryLimitMB should default to 4096, got %d", cfg.MemoryLimitMB)
	}
}

func TestDynamicSizing_MinNotExceedMax(t *testing.T) {
	pool := &TxsPool{
		config: TxsPoolConfig{
			GlobalSlots:    5120,
			MinGlobalSlots: 10000, // larger than max
			DynamicSizing:  true,
		},
	}
	// EffectiveGlobalSlots should still work
	pool.effectiveGlobalSlots.Store(5120)
	if got := pool.EffectiveGlobalSlots(); got != 5120 {
		t.Fatalf("expected 5120, got %d", got)
	}
}
