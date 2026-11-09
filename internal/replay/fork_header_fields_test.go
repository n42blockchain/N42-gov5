// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// Replay stamps the post-Prague fields the target config schedules: the slot
// (the target number) from Glamsterdam, and the source's registry root (or
// the last one seen) once MobileAnchor and Glamsterdam are both active.
func TestStampForkHeaderFields(t *testing.T) {
	e := &EngineV2{cfg: ConfigV2{ChainConfig: &params.ChainConfig{
		MobileAnchorTime: big.NewInt(10),
		GlamsterdamTime:  big.NewInt(100),
	}}}
	root := types.Hash{0x42}

	// Before Glamsterdam: nothing, even with a source root (chain 94's history).
	h := &block.Header{Number: uint256.NewInt(5), Time: 50}
	if err := e.stampForkHeaderFields(h, &block.Header{MobileRegistryRoot: &root}); err != nil {
		t.Fatal(err)
	}
	if h.SlotNumber != nil || h.MobileRegistryRoot != nil {
		t.Fatalf("pre-Glamsterdam: slot=%v root=%v", h.SlotNumber, h.MobileRegistryRoot)
	}

	// From Glamsterdam: slot = number; a gap block (no source) carries the
	// last source root.
	h = &block.Header{Number: uint256.NewInt(6), Time: 100}
	if err := e.stampForkHeaderFields(h, nil); err != nil {
		t.Fatal(err)
	}
	if h.SlotNumber == nil || *h.SlotNumber != 6 || h.MobileRegistryRoot == nil || *h.MobileRegistryRoot != root {
		t.Fatalf("post-fork gap block: slot=%v root=%v", h.SlotNumber, h.MobileRegistryRoot)
	}

	// A target that needs a root before the source ever had one is refused.
	fresh := &EngineV2{cfg: e.cfg}
	if err := fresh.stampForkHeaderFields(&block.Header{Number: uint256.NewInt(1), Time: 100}, &block.Header{}); err == nil {
		t.Fatal("stamped a root the source never had")
	}

	// No Glamsterdam in the target: the header ends at Prague.
	none := &EngineV2{cfg: ConfigV2{ChainConfig: &params.ChainConfig{MobileAnchorTime: big.NewInt(0)}}}
	h = &block.Header{Number: uint256.NewInt(7), Time: 1 << 40}
	if err := none.stampForkHeaderFields(h, &block.Header{MobileRegistryRoot: &root}); err != nil || h.SlotNumber != nil || h.MobileRegistryRoot != nil {
		t.Fatalf("no Glamsterdam: err=%v slot=%v root=%v", err, h.SlotNumber, h.MobileRegistryRoot)
	}
}
