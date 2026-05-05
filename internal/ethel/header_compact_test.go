// Copyright 2022-2026 The N42 Authors
package ethel

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"
	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// TestEncodeDecodeHeaderSegment_HashRoundtrip verifies the columnar
// segment round-trips Hash() byte-for-byte. The encoder embeds each
// header's canonical hash as a trailer column (hfStoredHash); the
// decoder must populate Header's hash atomic.Value via SetHash so the
// caller's hdr.Hash() returns canonical without recomputing — even
// though ParentHash and Bloom are dropped on disk.
func TestEncodeDecodeHeaderSegment_HashRoundtrip(t *testing.T) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()

	headers := []*block.Header{makeTestHeader(1), makeTestHeader(2), makeTestHeader(3)}
	canonicalHashes := make([]types.Hash, len(headers))
	for i, h := range headers {
		canonicalHashes[i] = h.Hash()
	}

	compressed := encodeHeaderSegment(headers, enc)

	dec, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	raw, err := dec.DecodeAll(compressed, nil)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := decodeHeaderSegment(raw)
	if err != nil {
		t.Fatalf("decodeHeaderSegment: %v", err)
	}
	if len(decoded) != len(headers) {
		t.Fatalf("count: got %d, want %d", len(decoded), len(headers))
	}
	for i, dh := range decoded {
		got := dh.Hash()
		if got != canonicalHashes[i] {
			t.Errorf("header[%d] Hash: got %x, want %x", i, got, canonicalHashes[i])
		}
	}
}

func makeTestHeader(seed byte) *block.Header {
	var ph types.Hash
	ph[0] = seed
	var bloom block.Bloom
	bloom[0] = seed * 7
	return &block.Header{
		ParentHash:  ph,
		Coinbase:    types.Address{seed, seed, seed},
		Root:        types.Hash{seed, seed},
		TxHash:      types.Hash{0, seed},
		ReceiptHash: types.Hash{seed, 0, seed},
		Bloom:       bloom,
		Difficulty:  uint256.NewInt(uint64(seed) * 1000),
		Number:      uint256.NewInt(uint64(seed)),
		GasLimit:    8_000_000,
		GasUsed:     uint64(seed) * 21000,
		Time:        1_700_000_000 + uint64(seed),
		Extra:       []byte{0x42, seed},
		MixDigest:   types.Hash{0, 0, seed},
		Nonce:       block.BlockNonce{0, seed},
		UncleHash:   types.Hash{0xFF, seed},
	}
}

// TestEncodeDecodeHeaderSegment_ZeroFieldsAfter verifies that the
// decoder leaves ParentHash and Bloom at zero on the round-tripped
// Header — they're not stored in the columnar format. Hash() must
// still return canonical because of the stored-hash trailer.
func TestEncodeDecodeHeaderSegment_ZeroFieldsAfter(t *testing.T) {
	enc, _ := zstd.NewWriter(nil)
	defer enc.Close()
	dec, _ := zstd.NewReader(nil)
	defer dec.Close()

	orig := makeTestHeader(5)
	canonical := orig.Hash()

	compressed := encodeHeaderSegment([]*block.Header{orig}, enc)
	raw, _ := dec.DecodeAll(compressed, nil)
	decoded, err := decodeHeaderSegment(raw)
	if err != nil {
		t.Fatal(err)
	}
	dh := decoded[0]
	var zero types.Hash
	if dh.ParentHash != zero {
		t.Errorf("ParentHash should be zero after roundtrip; got %x", dh.ParentHash)
	}
	if !bytes.Equal(dh.Bloom[:], make([]byte, len(dh.Bloom))) {
		t.Errorf("Bloom should be zero after roundtrip; got %x", dh.Bloom[:16])
	}
	if dh.Hash() != canonical {
		t.Errorf("Hash() should still equal canonical; got %x want %x", dh.Hash(), canonical)
	}
}
