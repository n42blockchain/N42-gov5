package sync

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	block "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// protoLegacyBlock renders a block the way a pre-RLP peer would put it on the
// wire: a bare types_pb.Block, with no trailer to carry the header fields
// protobuf has no slot for.
func protoLegacyBlock(t *testing.T, h *block.Header) []byte {
	t.Helper()
	b := block.NewBlock(h, nil)
	data, err := proto.Marshal(b.ToProtoMessage())
	if err != nil {
		t.Fatalf("proto marshal: %v", err)
	}
	return data
}

func legacyTestHeader() *block.Header {
	return &block.Header{
		ParentHash: types.HexToHash("0x01"),
		Root:       types.HexToHash("0x02"),
		TxHash:     types.HexToHash("0x03"),
		Number:     uint256.NewInt(1234),
		Time:       1784372100,
	}
}

// A header carrying MobileRegistryRoot cannot survive the protobuf wire form:
// types_pb.Header has no field for it, so the rebuilt header hashes
// differently from the one the network agreed on. The fallback must refuse
// rather than hand a mis-hashed block to the importer.
func TestDecodeChunkedBlockRefusesProtoWhenHeaderFieldsCanBeLost(t *testing.T) {
	h := legacyTestHeader()
	mrr := types.HexToHash("0xfeed")
	h.MobileRegistryRoot = &mrr
	want := h.Hash()

	data := protoLegacyBlock(t, h)

	// A chain that switches the field on: the fallback must stay closed.
	AllowLegacyProtoBlocks(&params.ChainConfig{MobileAnchorTime: big.NewInt(1784372000)})
	if blk, err := decodeChunkedBlock(data); err == nil {
		t.Fatalf("protobuf fallback accepted a lossy block: got hash %s, canonical %s",
			blk.Hash().Hex(), want.Hex())
	}

	// Same for a chain with EIP-7928 configured.
	AllowLegacyProtoBlocks(&params.ChainConfig{BALTime: big.NewInt(1)})
	if _, err := decodeChunkedBlock(data); err == nil {
		t.Fatal("protobuf fallback accepted a lossy block on a BAL chain")
	}

	// And with no chain config at all, which is the fail-closed default.
	AllowLegacyProtoBlocks(nil)
	if _, err := decodeChunkedBlock(data); err == nil {
		t.Fatal("protobuf fallback ran with no chain configured")
	}
}

// On a chain that carries neither field the fallback is lossless, and legacy
// peers must keep syncing: that is the compatibility case it exists for.
func TestDecodeChunkedBlockKeepsProtoWhereItIsLossless(t *testing.T) {
	h := legacyTestHeader()
	want := h.Hash()
	data := protoLegacyBlock(t, h)

	AllowLegacyProtoBlocks(&params.ChainConfig{})
	defer AllowLegacyProtoBlocks(nil)

	blk, err := decodeChunkedBlock(data)
	if err != nil {
		t.Fatalf("legacy peer rejected on a chain where protobuf is lossless: %v", err)
	}
	if got := blk.Hash(); got != want {
		t.Fatalf("rebuilt block hash %s, want %s", got.Hex(), want.Hex())
	}
}

// A pre-London header has no base fee, and its RLP omits the field. Turning an
// absent field into a present zero adds an element to the hash preimage, so the
// protobuf path silently rehashed every pre-London block -- which is exactly the
// history a legacy peer serves.
func TestDecodeChunkedBlockPreservesAbsentBaseFee(t *testing.T) {
	h := legacyTestHeader()
	h.Difficulty = uint256.NewInt(7)
	h.GasLimit = 30_000_000
	h.BaseFee = nil // pre-London
	want := h.Hash()

	AllowLegacyProtoBlocks(&params.ChainConfig{})
	defer AllowLegacyProtoBlocks(nil)

	blk, err := decodeChunkedBlock(protoLegacyBlock(t, h))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := blk.Hash(); got != want {
		t.Fatalf("pre-London block rehashed over the protobuf path: got %s want %s",
			got.Hex(), want.Hex())
	}
	if bf := blk.Header().(*block.Header).BaseFee; bf != nil {
		t.Fatalf("absent BaseFee came back as %v", bf)
	}
}
