// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package params

import (
	"math/big"
	"testing"
)

func TestIsPQTxGating(t *testing.T) {
	// Not configured → never active.
	cfgOff := &ChainConfig{}
	if cfgOff.IsPQTx(0) || cfgOff.IsPQTx(1<<62) {
		t.Fatal("IsPQTx should be false when PQTxTime is nil")
	}

	// Configured at t=1000 → false before, true at/after.
	cfg := &ChainConfig{PQTxTime: big.NewInt(1000)}
	if cfg.IsPQTx(999) {
		t.Fatal("IsPQTx(999) should be false (before activation)")
	}
	if !cfg.IsPQTx(1000) {
		t.Fatal("IsPQTx(1000) should be true (at activation)")
	}
	if !cfg.IsPQTx(2000) {
		t.Fatal("IsPQTx(2000) should be true (after activation)")
	}

	// Rules wiring carries it through.
	if !cfg.RulesWithTimestamp(1, 2000).IsPQTx {
		t.Fatal("Rules.IsPQTx should be true after activation")
	}
	if cfg.RulesWithTimestamp(1, 500).IsPQTx {
		t.Fatal("Rules.IsPQTx should be false before activation")
	}
}
