// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/rlp"
)

// The geth hash equivalence of an Amsterdam header (SlotNumber at RLP position
// 23) is pinned in internal/devp2p/header_amsterdam_geth_test.go, which can
// link go-ethereum.

// The N42 native chain stamps MobileRegistryRoot without a slot number. The
// root must survive the RLP and trailer round trips behind the empty
// position-23 placeholder, with an unchanged hash.
func TestHeaderMobileRegistryRootWithoutSlotRoundTrip(t *testing.T) {
	mrr := types.Hash{0x42}
	h := &Header{
		Difficulty:         uint256.NewInt(0),
		Number:             uint256.NewInt(14_300_000),
		Time:               1_790_000_000,
		BaseFee:            uint256.NewInt(7),
		MobileRegistryRoot: &mrr,
	}
	want := h.Hash()
	enc, err := rlp.EncodeToBytes(h)
	if err != nil {
		t.Fatal(err)
	}
	var back Header
	if err := rlp.DecodeBytes(enc, &back); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if back.SlotNumber != nil {
		t.Fatalf("SlotNumber = %d, want nil", *back.SlotNumber)
	}
	if back.MobileRegistryRoot == nil || *back.MobileRegistryRoot != mrr {
		t.Fatalf("MobileRegistryRoot = %v, want %s", back.MobileRegistryRoot, mrr.Hex())
	}
	if got := back.Hash(); got != want {
		t.Fatalf("hash after RLP round trip %s, want %s", got.Hex(), want.Hex())
	}

	m, err := h.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var fromTrailer Header
	if err := fromTrailer.Unmarshal(m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if fromTrailer.SlotNumber != nil || fromTrailer.MobileRegistryRoot == nil || fromTrailer.Hash() != want {
		t.Fatalf("trailer round trip: slot=%v mrr=%v hash=%s want %s",
			fromTrailer.SlotNumber, fromTrailer.MobileRegistryRoot, fromTrailer.Hash().Hex(), want.Hex())
	}
}

// A slot number set on its own (no MobileRegistryRoot) survives the trailer codec.
func TestHeaderSlotNumberTrailerRoundTrip(t *testing.T) {
	slot := uint64(13_456_789)
	h := &Header{Difficulty: uint256.NewInt(0), Number: uint256.NewInt(1), BaseFee: uint256.NewInt(7), SlotNumber: &slot}
	m, err := h.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var back Header
	if err := back.Unmarshal(m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.SlotNumber == nil || *back.SlotNumber != slot || back.Hash() != h.Hash() {
		t.Fatalf("trailer round trip: slot=%v hash=%s want %s", back.SlotNumber, back.Hash().Hex(), h.Hash().Hex())
	}
}
