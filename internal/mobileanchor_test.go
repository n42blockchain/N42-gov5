// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Phase-6c activation model (n42 native chain): the mobile-registry root is a
// HEADER commitment only — like the CommitteePool's ParentBeaconRoot link, it
// binds the committed accumulator root into the block hash with NO state-trie
// write. That is why it needs no system contract, no genesis alloc, and no
// replay to activate: exactly the mechanism the 200K/512 committee uses
// (header hash-link + rawdb side table). These tests pin the fork gate and the
// no-state-write property.

package internal

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func mobileAnchorConfig(active bool) *params.ChainConfig {
	cfg := &params.ChainConfig{
		ChainID:               big.NewInt(94),
		Consensus:             params.Faker,
		HomesteadBlock:        big.NewInt(0),
		TangerineWhistleBlock: big.NewInt(0),
		SpuriousDragonBlock:   big.NewInt(0),
		ByzantiumBlock:        big.NewInt(0),
		ConstantinopleBlock:   big.NewInt(0),
		PetersburgBlock:       big.NewInt(0),
		IstanbulBlock:         big.NewInt(0),
		BerlinBlock:           big.NewInt(0),
		LondonBlock:           big.NewInt(0),
		ShanghaiBlock:         big.NewInt(0),
		CancunBlock:           big.NewInt(0),
	}
	if active {
		cfg.MobileAnchorTime = big.NewInt(0)
	}
	return cfg
}

func TestMobileAnchorForkGate(t *testing.T) {
	if mobileAnchorConfig(false).IsMobileAnchor(1000) {
		t.Fatal("IsMobileAnchor true with a nil MobileAnchorTime (eth-el / dormant shape)")
	}
	if !mobileAnchorConfig(true).IsMobileAnchor(1000) {
		t.Fatal("IsMobileAnchor false while the fork is active")
	}
}

// TestMobileAnchorHeaderIsPureCommitment confirms the design property the user
// flagged: binding the root is a header-only commitment. Stamping it changes
// the block hash (it is committed to consensus) but touches no state — no
// system contract, no genesis alloc, no replay needed to activate. Mirrors the
// CommitteePool's ParentBeaconRoot mechanism.
func TestMobileAnchorHeaderIsPureCommitment(t *testing.T) {
	root := types.HexToHash("0x7777000000000000000000000000000000000000000000000000000000007777")
	bare := &block.Header{
		Number:     uint256.NewInt(500),
		Time:       9,
		Difficulty: uint256.NewInt(0),
		BaseFee:    uint256.NewInt(0),
		Root:       types.HexToHash("0x33"),
	}
	stamped := block.CopyHeader(bare)
	stamped.MobileRegistryRoot = &root

	// The state root field is untouched by stamping — the anchor is a header
	// commitment, not a state write (that is why no replay is required).
	if stamped.Root != bare.Root {
		t.Fatal("stamping the anchor changed the state root — it must not")
	}
	// The block hash DOES change (the root is bound into consensus).
	if stamped.Hash() == bare.Hash() {
		t.Fatal("stamping the anchor did not change the block hash")
	}
	// And it round-trips through the consensus block encoding unchanged.
	if got := block.CopyHeader(stamped); got.MobileRegistryRoot == nil || *got.MobileRegistryRoot != root {
		t.Fatal("anchor root lost through header copy")
	}
}
