// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/rlp"
)


func zeroHashP() *types.Hash { return new(types.Hash) }

// presentZeroBase is a header with every mandatory field set and no optional.
func presentZeroBase() *Header {
	return &Header{
		ParentHash: types.HexToHash("0x22"), Coinbase: types.HexToAddress("0x42"),
		Root: types.HexToHash("0x33"), TxHash: hash.EmptyRootHash, ReceiptHash: hash.EmptyRootHash,
		Difficulty: uint256.NewInt(0), Number: uint256.NewInt(7), GasLimit: 30_000_000,
		Time: 1_800_000_000, Extra: bytes.Repeat([]byte{0xab}, 97),
	}
}

// profile1Shape is the profile-1 header layout: every optional through
// SlotNumber plus MobileRegistryRoot, element 22 (BlockAccessListHash) a gap,
// the integer optionals and the hashes at zero.
func profile1Shape() *Header {
	h := presentZeroBase()
	h.BaseFee = uint256.NewInt(0)
	h.WithdrawalsHash = zeroHashP()
	h.BlobGasUsed, h.ExcessBlobGas = u64p(0), u64p(0)
	h.ParentBeaconRoot = zeroHashP()
	h.RequestsHash = zeroHashP()
	h.SlotNumber = u64p(7)
	h.MobileRegistryRoot = zeroHashP()
	return h
}

// allPresentZero sets every optional field, each to its zero value.
func allPresentZero() *Header {
	h := profile1Shape()
	h.BlockAccessListHash = zeroHashP()
	h.SlotNumber = u64p(0)
	return h
}

// requirePresence fails unless got has exactly want's optional fields, with
// equal values, and the same hash.
func requirePresence(t *testing.T, codec string, want, got *Header) {
	t.Helper()
	ptrU := func(name string, a, b *uint64) {
		if (a == nil) != (b == nil) || (a != nil && *a != *b) {
			t.Errorf("%s: %s = %v, want %v", codec, name, b, a)
		}
	}
	ptrH := func(name string, a, b *types.Hash) {
		if (a == nil) != (b == nil) || (a != nil && *a != *b) {
			t.Errorf("%s: %s = %v, want %v", codec, name, b, a)
		}
	}
	if (want.BaseFee == nil) != (got.BaseFee == nil) || (want.BaseFee != nil && !want.BaseFee.Eq(got.BaseFee)) {
		t.Errorf("%s: BaseFee = %v, want %v", codec, got.BaseFee, want.BaseFee)
	}
	ptrH("WithdrawalsHash", want.WithdrawalsHash, got.WithdrawalsHash)
	ptrU("BlobGasUsed", want.BlobGasUsed, got.BlobGasUsed)
	ptrU("ExcessBlobGas", want.ExcessBlobGas, got.ExcessBlobGas)
	ptrH("ParentBeaconRoot", want.ParentBeaconRoot, got.ParentBeaconRoot)
	ptrH("RequestsHash", want.RequestsHash, got.RequestsHash)
	ptrH("BlockAccessListHash", want.BlockAccessListHash, got.BlockAccessListHash)
	ptrU("SlotNumber", want.SlotNumber, got.SlotNumber)
	ptrH("MobileRegistryRoot", want.MobileRegistryRoot, got.MobileRegistryRoot)
	want.ResetHashCache()
	got.ResetHashCache()
	if want.Hash() != got.Hash() {
		t.Errorf("%s: hash %s, want %s", codec, got.Hash().Hex(), want.Hash().Hex())
	}
}

func rlpHeaderRoundTrip(t *testing.T, h *Header) *Header {
	t.Helper()
	enc, err := rlp.EncodeToBytes(h)
	if err != nil {
		t.Fatal(err)
	}
	var out Header
	if err := rlp.DecodeBytes(enc, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

func rlpBlockRoundTrips(t *testing.T, h *Header) map[string]*Header {
	t.Helper()
	enc, err := rlp.EncodeToBytes(NewBlock(h, nil))
	if err != nil {
		t.Fatal(err)
	}
	var plain Block
	if err := rlp.DecodeBytes(enc, &plain); err != nil {
		t.Fatal(err)
	}
	var pooled Block
	if _, _, err := pooled.DecodeRLPReusePool(rlp.NewStream(bytes.NewReader(enc), uint64(len(enc))), nil); err != nil {
		t.Fatal(err)
	}
	var viaUnmarshal Block
	if err := viaUnmarshal.Unmarshal(enc); err != nil {
		t.Fatal(err)
	}
	return map[string]*Header{
		"block RLP":           plain.header,
		"block RLP reuse":     pooled.header,
		"block Unmarshal RLP": viaUnmarshal.header,
	}
}

func storageRoundTrips(t *testing.T, h *Header) map[string]*Header {
	t.Helper()
	out := map[string]*Header{}
	pt, err := h.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var viaTrailer Header
	if err := viaTrailer.Unmarshal(pt); err != nil {
		t.Fatal(err)
	}
	out["proto+trailer"] = &viaTrailer
	var viaCompact Header
	if err := viaCompact.Unmarshal(h.MarshalCompact()); err != nil {
		t.Fatal(err)
	}
	out["compact"] = &viaCompact
	js, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	var viaJSON Header
	if err := json.Unmarshal(js, &viaJSON); err != nil {
		t.Fatal(err)
	}
	out["json"] = &viaJSON
	return out
}

// TestHeaderPresentZeroOptionalsEveryCodec: a header with every optional
// present at zero round-trips through every header codec with its fields and
// hash. Before the fix the RLP codecs (block push, fetch, peek) decoded
// BlobGasUsed = ExcessBlobGas = 0 as absent, which profile 1 refuses (B9).
func TestHeaderPresentZeroOptionalsEveryCodec(t *testing.T) {
	// Storage codecs carry presence explicitly: the full all-zero set.
	full := allPresentZero()
	for codec, got := range storageRoundTrips(t, full) {
		requirePresence(t, codec, full, got)
	}

	// RLP: the profile-1 shape (element-22 gap, SlotNumber and
	// MobileRegistryRoot after it) and the all-zero set without
	// MobileRegistryRoot, so the zero SlotNumber is the last element.
	slotLast := allPresentZero()
	slotLast.MobileRegistryRoot = nil
	for name, h := range map[string]*Header{"profile1": profile1Shape(), "slotLast": slotLast} {
		requirePresence(t, name+" header RLP", h, rlpHeaderRoundTrip(t, h))
		for codec, got := range rlpBlockRoundTrips(t, h) {
			requirePresence(t, name+" "+codec, h, got)
		}
		for codec, got := range storageRoundTrips(t, h) {
			requirePresence(t, name+" "+codec, h, got)
		}
	}
}

// TestHeaderRLPPresentZeroShapes covers each RLP rule and the gaps that must
// stay absent.
func TestHeaderRLPPresentZeroShapes(t *testing.T) {
	cases := map[string]func(h *Header){
		// Cancun only: the zero blob fields are the last elements.
		"cancunTrailing": func(h *Header) {
			h.BaseFee = uint256.NewInt(1)
			h.WithdrawalsHash = zeroHashP()
			h.BlobGasUsed, h.ExcessBlobGas = u64p(0), u64p(0)
		},
		// Cancun + Prague: zero blob fields in the middle.
		"prague": func(h *Header) {
			h.BaseFee = uint256.NewInt(1)
			h.WithdrawalsHash = zeroHashP()
			h.BlobGasUsed, h.ExcessBlobGas = u64p(0), u64p(0)
			h.ParentBeaconRoot = zeroHashP()
			h.RequestsHash = zeroHashP()
		},
		// One blob field non-zero: the other's zero is present.
		"blobUsedZeroExcessSet": func(h *Header) {
			h.BaseFee = uint256.NewInt(1)
			h.WithdrawalsHash = zeroHashP()
			h.BlobGasUsed, h.ExcessBlobGas = u64p(0), u64p(9)
			h.ParentBeaconRoot = zeroHashP()
		},
		// Committee link before Cancun: the blob fields are real gaps.
		"committeeLinkPreCancun": func(h *Header) {
			h.BaseFee = uint256.NewInt(1)
			h.WithdrawalsHash = zeroHashP()
			h.ParentBeaconRoot = zeroHashP()
		},
		// Mobile anchor before Amsterdam: BAL and SlotNumber are gaps.
		"mobileAnchorPreAmsterdam": func(h *Header) {
			h.BaseFee = uint256.NewInt(1)
			h.WithdrawalsHash = zeroHashP()
			h.BlobGasUsed, h.ExcessBlobGas = u64p(0), u64p(0)
			h.ParentBeaconRoot = zeroHashP()
			h.RequestsHash = zeroHashP()
			h.MobileRegistryRoot = zeroHashP()
		},
		// Mobile anchor before Cancun: every integer optional is a gap.
		"mobileAnchorPreCancun": func(h *Header) {
			h.BaseFee = uint256.NewInt(1)
			h.MobileRegistryRoot = zeroHashP()
		},
	}
	for name, mutate := range cases {
		h := presentZeroBase()
		mutate(h)
		requirePresence(t, name+" header RLP", h, rlpHeaderRoundTrip(t, h))
		for codec, got := range rlpBlockRoundTrips(t, h) {
			requirePresence(t, name+" "+codec, h, got)
		}
	}
}
