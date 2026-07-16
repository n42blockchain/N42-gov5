// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
)

func hashOf(b byte) types.Hash {
	var h types.Hash
	for i := range h {
		h[i] = b
	}
	return h
}

func u64p(v uint64) *uint64 { return &v }

// headersForCompactTest covers every field shape the codec distinguishes:
// defaults omitted vs present, nil vs zero optionals, constant vs custom hashes.
func headersForCompactTest() map[string]*Header {
	full := &Header{
		ParentHash:  hashOf(1),
		UncleHash:   hashOf(2), // non-constant
		Coinbase:    types.Address{0xaa, 0xbb},
		Root:        hashOf(3),
		TxHash:      hashOf(4),
		ReceiptHash: hashOf(5),
		Difficulty:  uint256.NewInt(123456),
		Number:      uint256.NewInt(9_999_999),
		GasLimit:    30_000_000,
		GasUsed:     12_345_678,
		Time:        1_678_855_970,
		Extra:       []byte("hello-extra-data"),
		MixDigest:   hashOf(6),
		Nonce:       BlockNonce{1, 2, 3, 4, 5, 6, 7, 8},
		BaseFee:     uint256.NewInt(7_000_000_000),
		BlobGasUsed: u64p(131072), ExcessBlobGas: u64p(0),
	}
	full.Bloom[0], full.Bloom[255] = 0x80, 0x01
	wh, pbr, rh := hashOf(7), hashOf(8), hashOf(9)
	full.WithdrawalsHash, full.ParentBeaconRoot, full.RequestsHash = &wh, &pbr, &rh
	// The trailing native optionals must survive the compact storage round-trip
	// too — a dropped field silently changes the stored header's hash and breaks
	// block import ("unknown ancestor"). MobileRegistryRoot regressed exactly
	// this way before the codec learned to carry it.
	bah, mrr := hashOf(14), hashOf(15)
	full.BlockAccessListHash, full.MobileRegistryRoot = &bah, &mrr

	emptyConst := hash.EmptyRootHash
	resealEmpty := &Header{ // the dominant shape in a resealed chain
		ParentHash: hashOf(10), UncleHash: hash.EmptyUncleHash,
		Root: hashOf(11), TxHash: hash.EmptyRootHash, ReceiptHash: hash.EmptyRootHash,
		Difficulty: uint256.NewInt(0), Number: uint256.NewInt(42),
		GasLimit: 30_000_000, Time: 1_678_855_978,
		Extra:           make([]byte, 32), // 32-zero vanity
		MixDigest:       hashOf(12),
		BaseFee:         uint256.NewInt(0), // present-but-zero (≠ nil!)
		WithdrawalsHash: &emptyConst, RequestsHash: &emptyConst,
		BlobGasUsed: u64p(0), ExcessBlobGas: u64p(0),
		ParentBeaconRoot: func() *types.Hash { h := hashOf(13); return &h }(),
	}

	minimal := &Header{ // everything default/nil
		ParentHash: hashOf(20), UncleHash: hash.EmptyUncleHash,
		Root: hashOf(21), TxHash: hash.EmptyRootHash, ReceiptHash: hash.EmptyRootHash,
		Difficulty: uint256.NewInt(0), Number: uint256.NewInt(0),
		GasLimit: 0, Time: 0,
	}

	return map[string]*Header{"full": full, "resealEmpty": resealEmpty, "minimal": minimal}
}

// TestCompactHeaderRoundTrip: decode(encode(h)) must reproduce every field and
// the RLP-derived block hash exactly, for all field shapes.
func TestCompactHeaderRoundTrip(t *testing.T) {
	for name, h := range headersForCompactTest() {
		enc := h.MarshalCompact()
		if !IsCompactHeader(enc) {
			t.Fatalf("%s: marker missing", name)
		}
		var got Header
		if err := got.Unmarshal(enc); err != nil { // via the dispatching Unmarshal
			t.Fatalf("%s: unmarshal: %v", name, err)
		}
		// Hash is RLP-derived from all consensus fields — the strongest equality.
		if got.Hash() != h.Hash() {
			t.Fatalf("%s: hash mismatch after round-trip:\n  want %x\n  got  %x", name, h.Hash(), got.Hash())
		}
		// Field-level checks for non-consensus-visible distinctions (nil vs zero).
		if (h.BaseFee == nil) != (got.BaseFee == nil) {
			t.Fatalf("%s: BaseFee nil-ness diverged", name)
		}
		if (h.BlobGasUsed == nil) != (got.BlobGasUsed == nil) {
			t.Fatalf("%s: BlobGasUsed nil-ness diverged", name)
		}
		if (h.WithdrawalsHash == nil) != (got.WithdrawalsHash == nil) {
			t.Fatalf("%s: WithdrawalsHash nil-ness diverged", name)
		}
		if !bytes.Equal(h.Extra, got.Extra) {
			t.Fatalf("%s: Extra diverged: %x vs %x", name, h.Extra, got.Extra)
		}
		if h.Bloom != got.Bloom || h.Nonce != got.Nonce || h.Coinbase != got.Coinbase {
			t.Fatalf("%s: bloom/nonce/coinbase diverged", name)
		}
	}
}

// TestCompactHeaderProtoCrossCheck: the same header stored via proto Marshal and
// via compact must decode (through the same Unmarshal) to the same hash — the
// mixed-table guarantee.
func TestCompactHeaderProtoCrossCheck(t *testing.T) {
	for name, h := range headersForCompactTest() {
		if h.BaseFee == nil {
			continue // pre-existing: the proto encoder requires BaseFee (nil panics);
			// real chain headers always carry it. Compact handles nil (round-trip test).
		}
		protoEnc, err := h.Marshal()
		if err != nil {
			t.Fatalf("%s: proto marshal: %v", name, err)
		}
		if IsCompactHeader(protoEnc) {
			t.Fatalf("%s: proto encoding collides with the compact marker", name)
		}
		var fromProto, fromCompact Header
		if err := fromProto.Unmarshal(protoEnc); err != nil {
			t.Fatalf("%s: proto unmarshal: %v", name, err)
		}
		if err := fromCompact.Unmarshal(h.MarshalCompact()); err != nil {
			t.Fatalf("%s: compact unmarshal: %v", name, err)
		}
		if fromProto.Hash() != fromCompact.Hash() {
			t.Fatalf("%s: proto vs compact decode hash mismatch", name)
		}
	}
}

// TestCompactHeaderSize: the dominant resealed-empty-block shape must be
// dramatically smaller than the proto encoding (the point of the codec).
func TestCompactHeaderSize(t *testing.T) {
	h := headersForCompactTest()["resealEmpty"]
	protoEnc, _ := h.Marshal()
	compactEnc := h.MarshalCompact()
	t.Logf("resealEmpty: proto=%dB compact=%dB (%.1fx)", len(protoEnc), len(compactEnc),
		float64(len(protoEnc))/float64(len(compactEnc)))
	if len(compactEnc)*3 > len(protoEnc) {
		t.Fatalf("compact not compact enough: proto=%d compact=%d", len(protoEnc), len(compactEnc))
	}
	// Truncated input must error, not panic or mis-decode.
	for cut := 1; cut < len(compactEnc); cut += 7 {
		var bad Header
		if err := bad.Unmarshal(compactEnc[:cut]); err == nil && cut < len(compactEnc) {
			// Some prefixes may parse if all remaining fields were optional —
			// but they must NOT reproduce the original hash.
			if bad.Hash() == h.Hash() {
				t.Fatalf("truncated@%d decoded to the same hash", cut)
			}
		}
	}
}
