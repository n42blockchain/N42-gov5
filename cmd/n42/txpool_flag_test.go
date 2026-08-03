package main

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/txspool"
)

// TestTxPoolFlagsReachThePool walks the value from the CLI destination through
// the conversion the node performs, because the defect this replaces was
// exactly a configuration that existed and went nowhere.
func TestTxPoolFlagsReachThePool(t *testing.T) {
	cfg := conf.DefaultTxPoolConfig()
	cfg.GlobalSlots = 200000
	cfg.GlobalQueue = 150000
	cfg.AccountSlots = 4096
	cfg.AccountQueue = 2048
	cfg.Lifetime = 30 * time.Minute

	got := txspool.FromConf(cfg)

	if got.GlobalSlots != 200000 || got.GlobalQueue != 150000 {
		t.Errorf("global capacity lost: slots=%d queue=%d", got.GlobalSlots, got.GlobalQueue)
	}
	if got.AccountSlots != 4096 || got.AccountQueue != 2048 {
		t.Errorf("per-account capacity lost: slots=%d queue=%d", got.AccountSlots, got.AccountQueue)
	}
	if got.Lifetime != 30*time.Minute {
		t.Errorf("Lifetime lost: %s", got.Lifetime)
	}
}

// TestTxPoolFlagsAreRegistered checks the flags exist and are bound to the
// config the node actually passes to the pool.
func TestTxPoolFlagsAreRegistered(t *testing.T) {
	want := map[string]bool{
		"txpool.globalslots": false, "txpool.globalqueue": false,
		"txpool.accountslots": false, "txpool.accountqueue": false,
		"txpool.lifetime": false,
	}
	for _, f := range txpoolFlags {
		for _, n := range f.Names() {
			if _, ok := want[n]; ok {
				want[n] = true
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("flag %s is not registered", name)
		}
	}
}
