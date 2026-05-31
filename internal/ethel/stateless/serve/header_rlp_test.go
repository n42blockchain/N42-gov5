package serve

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// TestHeaderRLPRoundTrip: the fork-aware header wire preserves Hash() exactly for
// pre-London (nil BaseFee — the case the proto path panics on), London, and Cancun
// headers. Hash() recompute equality is the real test (covers every hashed field +
// the fork-cumulative optional presence).
func TestHeaderRLPRoundTrip(t *testing.T) {
	h32 := func(b byte) types.Hash { var x types.Hash; x[31] = b; return x }
	ptr := func(b byte) *types.Hash { x := h32(b); return &x }
	u64 := func(v uint64) *uint64 { return &v }

	cases := map[string]*block.Header{
		"pre-london": { // 15 fields, BaseFee nil
			ParentHash: h32(1), UncleHash: h32(2), Root: h32(3), TxHash: h32(4),
			ReceiptHash: h32(5), Difficulty: uint256.NewInt(17179869184),
			Number: uint256.NewInt(990000), GasLimit: 8000000, GasUsed: 21000,
			Time: 1455404600, Extra: []byte("geth"), MixDigest: h32(6),
			Nonce: block.EncodeNonce(0x1234),
		},
		"london": { // +BaseFee
			ParentHash: h32(1), UncleHash: h32(2), Root: h32(3), TxHash: h32(4),
			ReceiptHash: h32(5), Difficulty: uint256.NewInt(1), Number: uint256.NewInt(13000000),
			GasLimit: 30000000, GasUsed: 15000000, Time: 1628166822, Extra: []byte{},
			MixDigest: h32(6), Nonce: block.EncodeNonce(0), BaseFee: uint256.NewInt(1000000000),
		},
		"cancun": { // +Withdrawals +blob trio
			ParentHash: h32(1), UncleHash: h32(2), Root: h32(3), TxHash: h32(4),
			ReceiptHash: h32(5), Difficulty: uint256.NewInt(0), Number: uint256.NewInt(19000000),
			GasLimit: 30000000, GasUsed: 15000000, Time: 1710000000, Extra: []byte{},
			MixDigest: h32(6), Nonce: block.EncodeNonce(0), BaseFee: uint256.NewInt(7),
			WithdrawalsHash: ptr(7), BlobGasUsed: u64(131072), ExcessBlobGas: u64(0),
			ParentBeaconRoot: ptr(8),
		},
	}
	for name, h := range cases {
		want := h.Hash()
		wire, err := HeaderToRLP(h)
		if err != nil {
			t.Fatalf("%s: encode: %v", name, err)
		}
		got, err := HeaderFromRLP(wire)
		if err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		if got.Hash() != want {
			t.Errorf("%s: hash %x != %x", name, got.Hash(), want)
		}
		if (got.BaseFee == nil) != (h.BaseFee == nil) {
			t.Errorf("%s: BaseFee nil-ness changed", name)
		}
	}
}
