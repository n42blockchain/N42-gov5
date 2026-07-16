// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Phase-6c constructive tests for the mobile-registry anchor system call.
// The property that guarantees no consensus fork: the call is a pure
// function of header.MobileRegistryRoot, so build and import — writing the
// same header field — produce byte-identical state. We prove that by
// checking the ring-buffer slots, plus the fork/nil no-op paths.

package internal

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
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

func newTestIBS(t *testing.T) *state.IntraBlockState {
	t.Helper()
	db := memdb.NewTestDB(t)
	txDb := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(txDb, 1))
	// Deploy the ring-buffer contract so the account is non-empty and its
	// storage survives EIP-158 finalization (production does this via genesis
	// alloc; without it the write is pruned, exactly like EIP-4788).
	ibs.CreateAccount(params.MobileAnchorAddress, true)
	ibs.SetCode(params.MobileAnchorAddress, params.MobileAnchorCode)
	return ibs
}

func readAnchorSlot(ibs *state.IntraBlockState, slotIndex uint64) types.Hash {
	slot := types.Hash{}
	uint256.NewInt(slotIndex).WriteToSlice(slot[:])
	var got uint256.Int
	ibs.GetState(params.MobileAnchorAddress, &slot, &got)
	out := types.Hash{}
	got.WriteToSlice(out[:])
	return out
}

func TestMobileAnchorWritesRingBuffer(t *testing.T) {
	cfg := mobileAnchorConfig(true)
	root := types.HexToHash("0xabc0000000000000000000000000000000000000000000000000000000000abc")
	const num = uint64(42)
	header := &block.Header{
		Number:             uint256.NewInt(num),
		Time:               100,
		MobileRegistryRoot: &root,
	}

	ibs := newTestIBS(t)
	if err := ProcessMobileRegistryAnchor(cfg, ibs, header, state.NewNoopWriter()); err != nil {
		t.Fatalf("anchor: %v", err)
	}

	buf := uint64(params.MobileAnchorHistoryBufferLen)
	// number slot holds the block number; root slot holds the root.
	gotNum := readAnchorSlot(ibs, num%buf)
	var wantNum types.Hash
	uint256.NewInt(num).WriteToSlice(wantNum[:])
	if gotNum != wantNum {
		t.Fatalf("number slot = %s, want %s", gotNum.Hex(), wantNum.Hex())
	}
	gotRoot := readAnchorSlot(ibs, (num%buf)+buf)
	if gotRoot != root {
		t.Fatalf("root slot = %s, want %s", gotRoot.Hex(), root.Hex())
	}
}

// TestMobileAnchorDeterministic is the no-fork guarantee: two independent
// states given the SAME header produce identical ring-buffer contents (build
// and import cannot diverge).
func TestMobileAnchorDeterministic(t *testing.T) {
	cfg := mobileAnchorConfig(true)
	root := types.HexToHash("0x1234000000000000000000000000000000000000000000000000000000001234")
	header := &block.Header{Number: uint256.NewInt(9000), Time: 5, MobileRegistryRoot: &root}

	a := newTestIBS(t)
	b := newTestIBS(t)
	if err := ProcessMobileRegistryAnchor(cfg, a, header, state.NewNoopWriter()); err != nil {
		t.Fatal(err)
	}
	if err := ProcessMobileRegistryAnchor(cfg, b, header, state.NewNoopWriter()); err != nil {
		t.Fatal(err)
	}
	buf := uint64(params.MobileAnchorHistoryBufferLen)
	slot := 9000 % buf
	if readAnchorSlot(a, slot) != readAnchorSlot(b, slot) ||
		readAnchorSlot(a, slot+buf) != readAnchorSlot(b, slot+buf) {
		t.Fatal("same header produced different anchor state — would fork consensus")
	}
}

func TestMobileAnchorNoopWhenForkInactive(t *testing.T) {
	cfg := mobileAnchorConfig(false) // MobileAnchorTime nil (eth-el shape)
	root := types.HexToHash("0xdead000000000000000000000000000000000000000000000000000000004dead")
	header := &block.Header{Number: uint256.NewInt(7), Time: 1, MobileRegistryRoot: &root}

	ibs := newTestIBS(t)
	if err := ProcessMobileRegistryAnchor(cfg, ibs, header, state.NewNoopWriter()); err != nil {
		t.Fatal(err)
	}
	buf := uint64(params.MobileAnchorHistoryBufferLen)
	if readAnchorSlot(ibs, 7%buf) != (types.Hash{}) || readAnchorSlot(ibs, (7%buf)+buf) != (types.Hash{}) {
		t.Fatal("anchor wrote state while the fork was inactive")
	}
}

func TestMobileAnchorNoopWhenRootNil(t *testing.T) {
	cfg := mobileAnchorConfig(true)
	header := &block.Header{Number: uint256.NewInt(3), Time: 1} // no MobileRegistryRoot
	ibs := newTestIBS(t)
	if err := ProcessMobileRegistryAnchor(cfg, ibs, header, state.NewNoopWriter()); err != nil {
		t.Fatal(err)
	}
	buf := uint64(params.MobileAnchorHistoryBufferLen)
	if readAnchorSlot(ibs, 3%buf) != (types.Hash{}) {
		t.Fatal("anchor wrote state with a nil root")
	}
}
