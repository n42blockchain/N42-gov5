// body_wire.go — single-block body wire codec for the stateless serving RPC.
// It reuses the SAME columnar segment codec (encodeBodySegment/decodeBodySegment)
// that produces the bodyc.*.cdat files and that the executor replays across the
// full chain, so a body served this way is faithful by construction — no
// geth-compatible header RLP encoder is needed (uncles ride as raw RLP).
package ethel

import (
	"fmt"

	"github.com/klauspost/compress/zstd"
)

// EncodeBodyBlock encodes one decoded body to the self-describing wire bytes the
// serve RPC ships (a one-block columnar segment; chainID is embedded). The output
// is zstd-framed exactly like a bodyc segment.
func EncodeBodyBlock(b *DecodedBlock, chainID uint64) ([]byte, error) {
	if b == nil {
		return nil, fmt.Errorf("ethel: nil body block")
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		return nil, err
	}
	defer enc.Close()
	return encodeBodySegment([]*DecodedBlock{b}, chainID, enc), nil
}

// DecodeBodyBlock decodes wire bytes produced by EncodeBodyBlock back into a
// DecodedBlock (the same struct ReadBody returns, so callers build a
// GethBodyResult identically). chainID is recovered from the segment header.
func DecodeBodyBlock(data []byte) (*DecodedBlock, error) {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	raw, err := dec.DecodeAll(data, nil)
	if err != nil {
		return nil, fmt.Errorf("ethel: decompress body wire: %w", err)
	}
	blocks, err := decodeBodySegment(raw)
	if err != nil {
		return nil, fmt.Errorf("ethel: decode body wire: %w", err)
	}
	if len(blocks) != 1 {
		return nil, fmt.Errorf("ethel: body wire has %d blocks, want 1", len(blocks))
	}
	return blocks[0], nil
}

// GethBodyFromDecoded builds the replay-ready GethBodyResult from a DecodedBlock,
// mirroring Executor.readBody (uncles via raw RLP, txs/withdrawals as decoded).
func GethBodyFromDecoded(d *DecodedBlock) *GethBodyResult {
	res := &GethBodyResult{Transactions: d.Txs, Withdrawals: d.Withdrawals}
	res.UncleRaw = d.UncleRLP
	return res
}
