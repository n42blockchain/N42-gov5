package devp2p

import (
	"bytes"
	"testing"

	gethp2p "github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"

	n42block "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/network/eth69"
)

type servingFakeProvider struct {
	headersByNumber map[uint64]*n42block.Header
	headersByHash   map[types.Hash]*n42block.Header
	bodies          map[types.Hash]*BlockBody
}

func (p *servingFakeProvider) CurrentHead() (*n42block.Header, types.Hash, error) {
	return nil, types.Hash{}, nil
}

func (p *servingFakeProvider) GetHeaderByNumber(number uint64) (*n42block.Header, error) {
	return p.headersByNumber[number], nil
}

func (p *servingFakeProvider) GetHeaderByHash(hash types.Hash) (*n42block.Header, error) {
	return p.headersByHash[hash], nil
}

func (p *servingFakeProvider) GetBlockBodyByHash(hash types.Hash) (*BlockBody, error) {
	return p.bodies[hash], nil
}

func TestHandleGetBlockHeadersServesRequestedRange(t *testing.T) {
	headers := make(map[uint64]*n42block.Header)
	byHash := make(map[types.Hash]*n42block.Header)
	for number := uint64(1); number <= 4; number++ {
		header := &n42block.Header{Number: uint256.NewInt(number), Difficulty: uint256.NewInt(0)}
		headers[number] = header
		byHash[header.Hash()] = header
	}
	provider := &servingFakeProvider{headersByNumber: headers, headersByHash: byHash}
	h := &EthHandler{provider: provider}
	req := &eth69.GetBlockHeadersPacket{RequestID: 42, GetBlockHeadersQuery: &eth69.GetBlockHeadersQuery{
		Origin: eth69.HashOrNumber{Hash: headers[1].Hash()}, Amount: 2, Skip: 1,
	}}
	encoded, err := rlp.EncodeToBytes(req)
	if err != nil {
		t.Fatal(err)
	}
	msg := gethp2p.Msg{Code: 3, Size: uint32(len(encoded)), Payload: bytes.NewReader(encoded)}
	rw1, rw2 := gethp2p.MsgPipe()
	defer rw1.Close()
	defer rw2.Close()
	errCh := make(chan error, 1)
	go func() { errCh <- h.handleGetBlockHeaders(rw1, msg) }()

	respMsg, err := rw2.ReadMsg()
	if err != nil {
		t.Fatal(err)
	}
	var resp blockHeadersPacket
	if err := respMsg.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if resp.RequestID != 42 || len(resp.Headers) != 2 {
		t.Fatalf("response = id %d, %d headers", resp.RequestID, len(resp.Headers))
	}
	for i, want := range []uint64{1, 3} {
		var got n42block.Header
		if err := rlp.DecodeBytes(resp.Headers[i], &got); err != nil {
			t.Fatal(err)
		}
		if got.Number.Uint64() != want {
			t.Fatalf("header %d number = %d, want %d", i, got.Number.Uint64(), want)
		}
	}
}

func TestHandleGetBlockBodiesServesUntilFirstMissing(t *testing.T) {
	h1 := types.HexToHash("0x01")
	h2 := types.HexToHash("0x02")
	provider := &servingFakeProvider{bodies: map[types.Hash]*BlockBody{
		h1: {Transactions: []rlp.RawValue{{0xc0}}, Uncles: []rlp.RawValue{}, Withdrawals: []rlp.RawValue{{0xc4, 0x01, 0x02, 0x80, 0x03}}},
	}}
	h := &EthHandler{provider: provider}
	encoded, err := rlp.EncodeToBytes(&getBlockBodiesPacket{RequestID: 7, Hashes: []types.Hash{h1, h2}})
	if err != nil {
		t.Fatal(err)
	}
	msg := gethp2p.Msg{Code: 5, Size: uint32(len(encoded)), Payload: bytes.NewReader(encoded)}
	rw1, rw2 := gethp2p.MsgPipe()
	defer rw1.Close()
	defer rw2.Close()
	errCh := make(chan error, 1)
	go func() { errCh <- h.handleGetBlockBodies(rw1, msg) }()

	respMsg, err := rw2.ReadMsg()
	if err != nil {
		t.Fatal(err)
	}
	var resp blockBodiesPacket
	if err := respMsg.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if resp.RequestID != 7 || len(resp.Bodies) != 1 {
		t.Fatalf("response = id %d, %d bodies", resp.RequestID, len(resp.Bodies))
	}
	if len(resp.Bodies[0].Withdrawals) != 1 {
		t.Fatalf("withdrawals = %d, want 1", len(resp.Bodies[0].Withdrawals))
	}
}
