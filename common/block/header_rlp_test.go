package block

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/rlp"
)

// sampleRLPHeaders returns headers across the three optional-field tiers
// (legacy/pre-London, London with BaseFee, Cancun with all post-merge fields),
// each carrying a 314-byte Extra like a sealed HotStuff header.
func sampleRLPHeaders() []*Header {
	base := func() *Header {
		return &Header{
			ParentHash:  types.HexToHash("0x22"),
			UncleHash:   types.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347"),
			Coinbase:    types.HexToAddress("0xf7dc5c92fa9e812eb0c3157492da65457ae5de46"),
			Root:        types.HexToHash("0x33"),
			TxHash:      types.HexToHash("0x44"),
			ReceiptHash: types.HexToHash("0x55"),
			Difficulty:  uint256.NewInt(0),
			Number:      uint256.NewInt(1001),
			GasLimit:    30000000,
			GasUsed:     21000,
			Time:        1700000000,
			Extra:       bytes.Repeat([]byte{0xab}, 314), // hotstuff: vanity+view+QC+BLS sig
			MixDigest:   types.HexToHash("0x66"),
		}
	}

	legacy := base()

	london := base()
	london.BaseFee = uint256.NewInt(1000000000)

	cancun := base()
	cancun.BaseFee = uint256.NewInt(1000000000)
	wh := types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	bgu := uint64(131072)
	ebg := uint64(262144)
	pbr := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	rh := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	cancun.WithdrawalsHash = &wh
	cancun.BlobGasUsed = &bgu
	cancun.ExcessBlobGas = &ebg
	cancun.ParentBeaconRoot = &pbr
	cancun.RequestsHash = &rh

	return []*Header{legacy, london, cancun}
}

// TestHeaderStructRLPMatchesManualHash is the critical regression guard for
// stage 0 of the RLP migration: encoding the Header struct via rlp (with the
// new optional tags) must yield the EXACT same keccak(rlp(header)) as the
// hand-written []interface{} slice in rlpHash(), or every historical block hash
// would shift.
func TestHeaderStructRLPMatchesManualHash(t *testing.T) {
	names := []string{"legacy", "london", "cancun"}
	for i, h := range sampleRLPHeaders() {
		h.ResetHashCache()
		manual := h.rlpHash()
		viaStruct := hash.RlpHash(h)
		if manual != viaStruct {
			t.Errorf("%s: hash.RlpHash(struct)=%s != rlpHash(manual)=%s",
				names[i], viaStruct.Hex(), manual.Hex())
		}
	}
}

// TestHeaderEncodeDecodeRLPRoundTrip verifies a struct-codec round trip
// preserves the hash for all three tiers.
func TestHeaderEncodeDecodeRLPRoundTrip(t *testing.T) {
	names := []string{"legacy", "london", "cancun"}
	for i, h := range sampleRLPHeaders() {
		h.ResetHashCache()
		want := h.Hash()

		enc, err := rlp.EncodeToBytes(h)
		if err != nil {
			t.Fatalf("%s: encode: %v", names[i], err)
		}
		var h2 Header
		if err := rlp.DecodeBytes(enc, &h2); err != nil {
			t.Fatalf("%s: decode: %v", names[i], err)
		}
		h2.ResetHashCache()
		if got := h2.Hash(); got != want {
			t.Errorf("%s: hash changed after RLP round trip: want %s got %s",
				names[i], want.Hex(), got.Hex())
		}
	}
}

// TestBlockRLPRoundTrip verifies a Block survives an RLP wire round trip with
// its hash unchanged (empty body; tx-bearing blocks are covered by the
// transaction package's own ETH RLP round-trip tests).
func TestBlockRLPRoundTrip(t *testing.T) {
	h := sampleRLPHeaders()[2] // cancun
	h.ResetHashCache()
	blk := NewBlock(h, nil).(*Block)
	want := blk.Hash()

	enc, err := rlp.EncodeToBytes(blk)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var blk2 Block
	if err := rlp.DecodeBytes(enc, &blk2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	blk2.header.ResetHashCache()
	if got := blk2.Hash(); got != want {
		t.Errorf("block hash changed after RLP round trip: want %s got %s", want.Hex(), got.Hex())
	}
}

// TestHeaderRLPDiscontinuousOptional guards the previously-divergent case: a
// header with a nil optional before a set one (BaseFee nil + WithdrawalsHash
// set). The old hand-written rlpHash() packed present optionals contiguously and
// disagreed with the struct codec here (different hash + a decode slot-shift);
// now that rlpHash() routes through the struct codec, Hash() and the
// EncodeRLP/DecodeRLP wire form agree by construction. This header isn't produced
// in practice but the codec must stay self-consistent.
func TestHeaderRLPDiscontinuousOptional(t *testing.T) {
	h := sampleRLPHeaders()[0] // legacy base: BaseFee nil
	wh := types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	h.WithdrawalsHash = &wh // set a later optional while BaseFee stays nil -> discontinuous
	h.ResetHashCache()
	want := h.Hash()

	enc, err := rlp.EncodeToBytes(h)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var h2 Header
	if err := rlp.DecodeBytes(enc, &h2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	h2.ResetHashCache()
	if got := h2.Hash(); got != want {
		t.Errorf("discontinuous-optional header hash changed after RLP round trip: want %s got %s", want.Hex(), got.Hex())
	}
	// The RLP struct codec encodes a nil *uint256.Int and a zero one identically
	// (empty string), so BaseFee decodes back as zero rather than nil — expected
	// and hash-neutral (the hash assert above passed). The key guarantee is that
	// WithdrawalsHash did NOT shift into the BaseFee slot — the exact corruption
	// the old hand-written rlpHash produced on a discontinuous optional.
	if h2.WithdrawalsHash == nil || *h2.WithdrawalsHash != wh {
		t.Errorf("WithdrawalsHash lost/shifted after round trip: %v", h2.WithdrawalsHash)
	}
}
