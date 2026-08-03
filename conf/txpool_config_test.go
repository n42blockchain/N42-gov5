// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package conf

import (
	"testing"
	"time"
)

// TestDefaultTxPoolConfigIsUsable pins the shape of the built-in defaults. They
// are what a node runs with when nothing is configured, and for a long time
// they were what it ran with even when something WAS configured, because no
// path reached the pool.
func TestDefaultTxPoolConfigIsUsable(t *testing.T) {
	c := DefaultTxPoolConfig()
	if c.GlobalSlots == 0 || c.GlobalQueue == 0 || c.AccountSlots == 0 || c.AccountQueue == 0 {
		t.Fatal("a default capacity is zero; the pool would reject everything")
	}
	if c.Lifetime <= 0 {
		t.Fatal("Lifetime defaults to zero, which disables the only reclamation path for queued transactions")
	}
	if c.MinGlobalSlots > c.GlobalSlots {
		t.Fatal("dynamic sizing floor is above its ceiling")
	}
}

// TestSanitizeRepairsUnusableValues covers a config file that sets a capacity
// to zero. Left alone that silently disables the pool; the node should come up
// on the defaults and say what it changed.
func TestSanitizeRepairsUnusableValues(t *testing.T) {
	c := TxPoolConfig{} // everything zero, as an empty config section yields
	fixed := c.Sanitize()

	if len(fixed) == 0 {
		t.Fatal("Sanitize reported no changes for an all-zero config")
	}
	def := DefaultTxPoolConfig()
	if c.GlobalSlots != def.GlobalSlots || c.GlobalQueue != def.GlobalQueue {
		t.Errorf("capacities not repaired: slots=%d queue=%d", c.GlobalSlots, c.GlobalQueue)
	}
	if c.AccountSlots != def.AccountSlots || c.AccountQueue != def.AccountQueue {
		t.Error("per-account capacities not repaired")
	}
}

// TestSanitizeLeavesLifetimeAlone keeps the escape hatch: zero Lifetime means
// "do not age queued transactions out", which is a choice, unlike a zero
// capacity which is just broken.
func TestSanitizeLeavesLifetimeAlone(t *testing.T) {
	c := DefaultTxPoolConfig()
	c.Lifetime = 0
	c.Sanitize()
	if c.Lifetime != 0 {
		t.Errorf("Sanitize overrode a deliberate zero Lifetime with %s", c.Lifetime)
	}
}

// TestSanitizeClampsDynamicFloor covers a floor set above the ceiling, which
// would otherwise make dynamic sizing scale capacity upward.
func TestSanitizeClampsDynamicFloor(t *testing.T) {
	c := DefaultTxPoolConfig()
	c.GlobalSlots = 2048
	c.MinGlobalSlots = 8192
	c.Sanitize()
	if c.MinGlobalSlots > c.GlobalSlots {
		t.Errorf("floor %d still above ceiling %d", c.MinGlobalSlots, c.GlobalSlots)
	}
}

// TestSanitizeKeepsExplicitValues makes sure a configured pool is not quietly
// replaced by the defaults -- the failure the whole change exists to prevent.
func TestSanitizeKeepsExplicitValues(t *testing.T) {
	c := TxPoolConfig{
		PriceLimit: 7, PriceBump: 20,
		AccountSlots: 4096, GlobalSlots: 200000,
		AccountQueue: 4096, GlobalQueue: 200000,
		Lifetime:       time.Hour,
		MinGlobalSlots: 1024, MemoryLimitMB: 8192,
	}
	if fixed := c.Sanitize(); len(fixed) != 0 {
		t.Fatalf("Sanitize changed a fully specified config: %v", fixed)
	}
	if c.GlobalSlots != 200000 || c.Lifetime != time.Hour {
		t.Error("explicit values did not survive Sanitize")
	}
}
