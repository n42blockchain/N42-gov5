// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Phase-6c header field: the mobile-registry accumulator root (n42 native
// chain). Mirrors the BAL header tests — pre/post-fork hash invariance,
// trailer round trip, and consensus-RLP round trip.

package block

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/rlp"
)

func TestHeaderMobileAnchorParticipatesInHash(t *testing.T) {
	preFork := balSampleHeader()
	preForkHash := preFork.Hash()

	// Nil MobileRegistryRoot → identical hash (trailing optional omitted).
	control := balSampleHeader()
	if control.Hash() != preForkHash {
		t.Fatalf("nil MobileRegistryRoot not stable: %s vs %s", control.Hash().Hex(), preForkHash.Hex())
	}

	// Bind a root → the header hash must change (it is in consensus).
	withRoot := balSampleHeader()
	root := types.HexToHash("0xabc0000000000000000000000000000000000000000000000000000000000abc")
	withRoot.MobileRegistryRoot = &root
	if withRoot.Hash() == preForkHash {
		t.Fatal("binding MobileRegistryRoot did not change the header hash")
	}

	// It is independent of BlockAccessListHash (both can coexist).
	both := balSampleHeader()
	blah := types.HexToHash("0x99aabbccddeeff00112233445566778899aabbccddeeff0011223344556677ff")
	both.BlockAccessListHash = &blah
	both.MobileRegistryRoot = &root
	if both.Hash() == withRoot.Hash() {
		t.Fatal("BlockAccessListHash and MobileRegistryRoot collapsed to the same hash")
	}
}

func TestHeaderMobileAnchorMarshalTrailerRoundTrip(t *testing.T) {
	h := balSampleHeader()
	root := types.HexToHash("0xabc0000000000000000000000000000000000000000000000000000000000abc")
	h.MobileRegistryRoot = &root
	want := h.Hash()

	enc, err := h.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var h2 Header
	if err := h2.Unmarshal(enc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if h2.MobileRegistryRoot == nil || *h2.MobileRegistryRoot != root {
		t.Fatalf("MobileRegistryRoot not preserved through Marshal trailer: %v", h2.MobileRegistryRoot)
	}
	if h2.Hash() != want {
		t.Fatalf("hash changed through Marshal round trip: want %s got %s", want.Hex(), h2.Hash().Hex())
	}
	if IsLegacyHeader(&h2) {
		t.Fatal("header with MobileRegistryRoot must not be classified as legacy")
	}
}

func TestHeaderMobileAnchorRLPRoundTrip(t *testing.T) {
	h := balSampleHeader()
	root := types.HexToHash("0xabc0000000000000000000000000000000000000000000000000000000000abc")
	h.MobileRegistryRoot = &root
	want := h.Hash()

	enc, err := rlp.EncodeToBytes(h)
	if err != nil {
		t.Fatalf("rlp encode: %v", err)
	}
	var h2 Header
	if err := rlp.DecodeBytes(enc, &h2); err != nil {
		t.Fatalf("rlp decode: %v", err)
	}
	if h2.MobileRegistryRoot == nil || *h2.MobileRegistryRoot != root {
		t.Fatalf("MobileRegistryRoot not carried through consensus RLP: %v", h2.MobileRegistryRoot)
	}
	if h2.Hash() != want {
		t.Fatalf("hash changed through RLP round trip: want %s got %s", want.Hex(), h2.Hash().Hex())
	}
}

// TestHeaderMobileAnchorCopyHeader confirms CopyHeader deep-copies the field.
func TestHeaderMobileAnchorCopyHeader(t *testing.T) {
	h := balSampleHeader()
	root := types.HexToHash("0xabc0000000000000000000000000000000000000000000000000000000000abc")
	h.MobileRegistryRoot = &root
	cpy := CopyHeader(h)
	if cpy.MobileRegistryRoot == nil || *cpy.MobileRegistryRoot != root {
		t.Fatal("CopyHeader dropped MobileRegistryRoot")
	}
	if cpy.MobileRegistryRoot == h.MobileRegistryRoot {
		t.Fatal("CopyHeader aliased MobileRegistryRoot instead of deep-copying")
	}
}
