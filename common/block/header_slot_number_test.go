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

// Binding a slot number changes the hash, with and without a registry root,
// and a header with both has 24 elements: the slot at 23, the root at 24.
func TestHeaderSlotNumberParticipatesInHash(t *testing.T) {
	slot := uint64(1001)
	root := types.HexToHash("0xabc0000000000000000000000000000000000000000000000000000000000abc")

	plain := balSampleHeader()
	withSlot := balSampleHeader()
	withSlot.SlotNumber = &slot
	if withSlot.Hash() == plain.Hash() {
		t.Fatal("binding SlotNumber did not change the header hash")
	}
	both := balSampleHeader()
	both.SlotNumber = &slot
	both.MobileRegistryRoot = &root
	if both.Hash() == withSlot.Hash() {
		t.Fatal("MobileRegistryRoot behind the slot does not bind the hash")
	}
	if n := headerElementCount(t, withSlot); n != 23 {
		t.Fatalf("slot without root encodes %d elements, want 23", n)
	}
	if n := headerElementCount(t, both); n != 24 {
		t.Fatalf("slot with root encodes %d elements, want 24", n)
	}
}

// A slot plus a registry root round-trips through RLP and both storage
// codecs with its hash, with and without a BAL hash at 22.
func TestHeaderSlotNumberAndMobileRegistryRootRoundTrip(t *testing.T) {
	slot := uint64(2_000_000)
	root := types.HexToHash("0x4242424242424242424242424242424242424242424242424242424242424242")
	bal := types.HexToHash("0x99aabbccddeeff00112233445566778899aabbccddeeff0011223344556677ff")
	for _, withBAL := range []bool{false, true} {
		h := balSampleHeader()
		h.SlotNumber = &slot
		h.MobileRegistryRoot = &root
		if withBAL {
			h.BlockAccessListHash = &bal
		}
		want := h.Hash()

		enc, err := rlp.EncodeToBytes(h)
		if err != nil {
			t.Fatal(err)
		}
		var back Header
		if err := rlp.DecodeBytes(enc, &back); err != nil {
			t.Fatalf("decode (bal %v): %v", withBAL, err)
		}
		if back.SlotNumber == nil || *back.SlotNumber != slot || back.MobileRegistryRoot == nil || *back.MobileRegistryRoot != root {
			t.Fatalf("RLP round trip (bal %v): slot=%v root=%v", withBAL, back.SlotNumber, back.MobileRegistryRoot)
		}
		if (back.BlockAccessListHash != nil) != withBAL || back.Hash() != want {
			t.Fatalf("RLP round trip (bal %v): bal=%v hash %s, want %s", withBAL, back.BlockAccessListHash, back.Hash().Hex(), want.Hex())
		}

		m, err := h.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		var fromTrailer Header
		if err := fromTrailer.Unmarshal(m); err != nil {
			t.Fatal(err)
		}
		if fromTrailer.Hash() != want {
			t.Fatalf("trailer round trip (bal %v) hash %s, want %s", withBAL, fromTrailer.Hash().Hex(), want.Hex())
		}
		var fromCompact Header
		if err := fromCompact.Unmarshal(h.MarshalCompact()); err != nil {
			t.Fatal(err)
		}
		if fromCompact.Hash() != want {
			t.Fatalf("compact round trip (bal %v) hash %s, want %s", withBAL, fromCompact.Hash().Hex(), want.Hex())
		}
	}
}

func headerElementCount(t *testing.T, h *Header) int {
	t.Helper()
	enc, err := rlp.EncodeToBytes(h)
	if err != nil {
		t.Fatal(err)
	}
	content, _, err := rlp.SplitList(enc)
	if err != nil {
		t.Fatal(err)
	}
	n, err := rlp.CountValues(content)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
