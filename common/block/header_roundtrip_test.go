package block

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/proto/types_pb"
)

// TestHeaderProtoRoundtripHash reproduces the HotStuff direct-push / fetch-on-miss
// bug: a block's hash changes after a proto encode/decode round trip, so a
// follower never agrees on the proposed block's hash.
func TestHeaderProtoRoundtripHash(t *testing.T) {
	wh := types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	pbr := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	bgu := uint64(0)
	ebg := uint64(0)
	h := &Header{
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
		Extra:       bytes.Repeat([]byte{0xab}, 200), // hotstuff: vanity+view+QC+BLS sig
		MixDigest:   types.HexToHash("0x66"),
		BaseFee:     uint256.NewInt(1000000000),
		WithdrawalsHash:  &wh,
		BlobGasUsed:      &bgu,
		ExcessBlobGas:    &ebg,
		ParentBeaconRoot: &pbr,
	}
	want := h.Hash()

	pb := h.ToProtoMessage()
	// Block p2p transport now uses proto.Marshal (NOT SSZ — see WriteBlockChunk),
	// because the generated SSZ schema drops Cancun/Shanghai header fields and
	// caps Extra at 117 bytes, changing the hash. Verify proto preserves the hash
	// for a HotStuff-sized header (large Extra + Cancun fields set).
	data, err := proto.Marshal(pb)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	pb2 := &types_pb.Header{}
	if err := proto.Unmarshal(data, pb2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	var h2 Header
	if err := h2.FromProtoMessage(pb2); err != nil {
		t.Fatalf("FromProtoMessage: %v", err)
	}
	got := h2.Hash()

	if got == want {
		return // round trip preserves the hash
	}
	t.Errorf("hash changed after proto round trip: want %s got %s", want.Hex(), got.Hex())
	// Pinpoint the differing field.
	if h.ParentHash != h2.ParentHash {
		t.Errorf("  ParentHash: %v -> %v", h.ParentHash, h2.ParentHash)
	}
	if h.UncleHash != h2.UncleHash {
		t.Errorf("  UncleHash: %v -> %v", h.UncleHash, h2.UncleHash)
	}
	if h.Coinbase != h2.Coinbase {
		t.Errorf("  Coinbase: %v -> %v", h.Coinbase, h2.Coinbase)
	}
	if h.Difficulty.Cmp(h2.Difficulty) != 0 {
		t.Errorf("  Difficulty: %v -> %v", h.Difficulty, h2.Difficulty)
	}
	if h.Number.Cmp(h2.Number) != 0 {
		t.Errorf("  Number: %v -> %v", h.Number, h2.Number)
	}
	if h.Time != h2.Time {
		t.Errorf("  Time: %v -> %v", h.Time, h2.Time)
	}
	if !bytes.Equal(h.Extra, h2.Extra) {
		t.Errorf("  Extra: len %d -> len %d", len(h.Extra), len(h2.Extra))
	}
	if h.MixDigest != h2.MixDigest {
		t.Errorf("  MixDigest: %v -> %v", h.MixDigest, h2.MixDigest)
	}
	if h.Nonce != h2.Nonce {
		t.Errorf("  Nonce: %v -> %v", h.Nonce, h2.Nonce)
	}
	if (h.BaseFee == nil) != (h2.BaseFee == nil) || (h.BaseFee != nil && h.BaseFee.Cmp(h2.BaseFee) != 0) {
		t.Errorf("  BaseFee: %v -> %v", h.BaseFee, h2.BaseFee)
	}
	if (h.WithdrawalsHash == nil) != (h2.WithdrawalsHash == nil) {
		t.Errorf("  WithdrawalsHash nil: %v -> %v", h.WithdrawalsHash == nil, h2.WithdrawalsHash == nil)
	}
	if (h.BlobGasUsed == nil) != (h2.BlobGasUsed == nil) {
		t.Errorf("  BlobGasUsed nil: %v -> %v", h.BlobGasUsed == nil, h2.BlobGasUsed == nil)
	}
	if (h.ExcessBlobGas == nil) != (h2.ExcessBlobGas == nil) {
		t.Errorf("  ExcessBlobGas nil: %v -> %v", h.ExcessBlobGas == nil, h2.ExcessBlobGas == nil)
	}
	if (h.ParentBeaconRoot == nil) != (h2.ParentBeaconRoot == nil) {
		t.Errorf("  ParentBeaconRoot nil: %v -> %v", h.ParentBeaconRoot == nil, h2.ParentBeaconRoot == nil)
	}
	if (h.RequestsHash == nil) != (h2.RequestsHash == nil) {
		t.Errorf("  RequestsHash nil: %v -> %v", h.RequestsHash == nil, h2.RequestsHash == nil)
	}
}
