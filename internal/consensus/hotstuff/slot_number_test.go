// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/params"
)

// EIP-7843 on the native chain: the slot number is the block number, present
// from Glamsterdam on and absent before it.
func TestVerifySlotNumberByFork(t *testing.T) {
	cfg := &params.ChainConfig{GlamsterdamTime: big.NewInt(1000)}
	slot := func(v uint64) *uint64 { return &v }
	cases := []struct {
		name string
		time uint64
		slot *uint64
		ok   bool
	}{
		{"before the fork, no slot", 999, nil, true},
		{"before the fork, a slot", 999, slot(7), false},
		{"at the fork, no slot", 1000, nil, false},
		{"at the fork, the block number", 1000, slot(7), true},
		{"at the fork, another value", 1000, slot(8), false},
	}
	for _, c := range cases {
		h := &block.Header{Number: uint256.NewInt(7), Time: c.time, SlotNumber: c.slot}
		err := verifySlotNumber(cfg, h)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
	// A chain that never schedules Glamsterdam never carries the field.
	if err := verifySlotNumber(&params.ChainConfig{}, &block.Header{Number: uint256.NewInt(7), Time: 1 << 40}); err != nil {
		t.Errorf("no Glamsterdam: %v", err)
	}
}

// The registry root is in the header only under both forks.
func TestMobileRegistryRootNeedsGlamsterdam(t *testing.T) {
	cfg := &params.ChainConfig{MobileAnchorTime: big.NewInt(50), GlamsterdamTime: big.NewInt(100)}
	for _, c := range []struct {
		time uint64
		want bool
	}{{49, false}, {50, false}, {99, false}, {100, true}} {
		if got := cfg.IsMobileRegistryRootInHeader(c.time); got != c.want {
			t.Errorf("time %d: %v, want %v", c.time, got, c.want)
		}
	}
	if (&params.ChainConfig{GlamsterdamTime: big.NewInt(0)}).IsMobileRegistryRootInHeader(1) {
		t.Error("root in the header without MobileAnchor")
	}
}
