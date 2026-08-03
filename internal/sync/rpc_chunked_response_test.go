package sync

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"

	types "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	comtypes "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/lib/rlp"
)

// bigBlock builds a block whose RLP encoding exceeds encoder.MaxChunkSize
// (1 MiB), which is what any block carrying a few thousand transactions does.
func bigBlock(t *testing.T) types.IBlock {
	t.Helper()
	to := comtypes.Address{0x11, 0x22}
	txs := make([]*transaction.Transaction, 0, 6000)
	for i := 0; i < 6000; i++ {
		txs = append(txs, transaction.NewTx(&transaction.DynamicFeeTx{
			ChainID:   uint256.NewInt(94),
			Nonce:     uint64(i),
			GasTipCap: uint256.NewInt(1),
			GasFeeCap: uint256.NewInt(1e10),
			Gas:       21000,
			To:        &to,
			Value:     uint256.NewInt(1),
			Data:      bytes.Repeat([]byte{byte(i)}, 128),
		}))
	}
	h := &types.Header{
		ParentHash:  comtypes.HexToHash("0x22"),
		UncleHash:   comtypes.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347"),
		Coinbase:    comtypes.HexToAddress("0xf7dc5c92fa9e812eb0c3157492da65457ae5de46"),
		Root:        comtypes.HexToHash("0x33"),
		TxHash:      comtypes.HexToHash("0x44"),
		ReceiptHash: comtypes.HexToHash("0x55"),
		Difficulty:  uint256.NewInt(0),
		Number:      uint256.NewInt(13273239),
		GasLimit:    480000000,
		GasUsed:     126000000,
		Time:        1700000000,
		Extra:       bytes.Repeat([]byte{0xab}, 200), // hotstuff: vanity+view+QC+BLS sig
		MixDigest:   comtypes.HexToHash("0x66"),
		BaseFee:     uint256.NewInt(1000000000),
	}
	blk := types.NewBlock(h, txs)
	raw, err := rlp.EncodeToBytes(blk)
	if err != nil {
		t.Fatalf("rlp encode: %v", err)
	}
	if uint64(len(raw)) <= encoder.MaxChunkSize {
		t.Fatalf("test block is %d bytes, needs to exceed MaxChunkSize %d to be meaningful",
			len(raw), encoder.MaxChunkSize)
	}
	return blk
}

// TestWriteBlockChunkCarriesOversizeBlock pins the responder and the requester
// to the same size cap.
//
// The responder used to frame the payload with the default MaxChunkSize (1 MiB)
// while the requester decoded with MaxBlockChunkSize (64 MiB). Every block
// larger than 1 MiB therefore failed to encode -- after the result code and
// fork digest had already gone out -- and the requester saw five valid bytes
// followed by nothing, which it reported as "snappy: corrupt input". Direct
// push already used the block cap, so the chain kept producing while any node
// that fell behind across such blocks could never catch up.
func TestWriteBlockChunkCarriesOversizeBlock(t *testing.T) {
	blk := bigBlock(t)
	genesis := comtypes.HexToHash("0xa2d2ff5d00000000000000000000000000000000000000000000000000000000")

	var buf bytes.Buffer
	if err := writeBlockChunk(&buf, genesis, blk); err != nil {
		t.Fatalf("writeBlockChunk: %v", err)
	}

	// Mirror readFirstChunkedBlock's framing: result code, fork digest, payload.
	if got := buf.Next(1); len(got) != 1 || got[0] != responseCodeSuccess {
		t.Fatalf("result code = %v, want %d", got, responseCodeSuccess)
	}
	if got := buf.Next(forkDigestLength); len(got) != forkDigestLength {
		t.Fatalf("short fork digest: %d bytes", len(got))
	}
	raw := &rawSSZBytes{}
	if err := encoder.DecodeWithMaxLengthLimit(&buf, raw, encoder.MaxBlockChunkSize); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var got types.Block
	if err := rlp.DecodeBytes(raw.data, &got); err != nil {
		t.Fatalf("rlp decode: %v", err)
	}
	if got.Hash() != blk.Hash() {
		t.Fatalf("hash = %s, want %s", got.Hash(), blk.Hash())
	}
	if len(got.Transactions()) != len(blk.Transactions()) {
		t.Fatalf("tx count = %d, want %d", len(got.Transactions()), len(blk.Transactions()))
	}
}

// TestWriteBlockChunkWritesNothingWhenPayloadTooLarge covers the other half of
// the failure: a responder that cannot produce the payload must not leave a
// half-written chunk on the stream, because the requester cannot tell that
// apart from corruption and retries against every peer with the same result.
func TestWriteBlockChunkWritesNothingWhenPayloadTooLarge(t *testing.T) {
	saved := encoder.MaxBlockChunkSize
	encoder.MaxBlockChunkSize = 1024
	defer func() { encoder.MaxBlockChunkSize = saved }()

	var buf bytes.Buffer
	err := writeBlockChunk(&buf, comtypes.Hash{}, bigBlock(t))
	if err == nil {
		t.Fatal("expected an error for a payload over the cap")
	}
	// Nothing at all, not "no payload": a result code and fork digest with no
	// payload behind them is exactly what stranded the laggard. The requester
	// consumed those five bytes, then read the following error response's code
	// byte (0x02) as the payload's length prefix and reported the two bytes
	// after it as "snappy: corrupt input" -- against every peer, forever.
	if buf.Len() != 0 {
		t.Fatalf("wrote %d bytes after failing; a chunk that cannot be completed "+
			"must not put its header on the stream", buf.Len())
	}
}
