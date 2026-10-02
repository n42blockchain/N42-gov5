package devp2p

import (
	"bytes"
	"testing"

	gethp2p "github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/rlp"

	n42block "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/network/eth69"
)

// dispatchFakeResponseHandler records every callback handleMessage routes to it,
// including the optional newBlockHashesHandler and balResponseHandler capabilities.
type dispatchFakeResponseHandler struct {
	newBlockHashes []eth69.NewBlockHashesPacket
	headers        [][][]byte
	bodies         [][]BlockBody
	receipts       [][][]rlp.RawValue
	bals           [][][]byte
}

func (f *dispatchFakeResponseHandler) OnPeerHandshake(string, gethp2p.MsgReadWriter, uint64, types.Hash, uint) {
}
func (f *dispatchFakeResponseHandler) OnPeerDisconnect(string) {}
func (f *dispatchFakeResponseHandler) OnBlockHeaders(_ string, _ uint64, headers [][]byte) {
	f.headers = append(f.headers, headers)
}
func (f *dispatchFakeResponseHandler) OnBlockBodies(_ string, _ uint64, bodies []BlockBody) {
	f.bodies = append(f.bodies, bodies)
}
func (f *dispatchFakeResponseHandler) OnReceipts(_ string, _ uint64, receipts [][]rlp.RawValue) {
	f.receipts = append(f.receipts, receipts)
}
func (f *dispatchFakeResponseHandler) OnNewBlock(string, *n42block.Header, [][]byte) {}
func (f *dispatchFakeResponseHandler) OnNewBlockHashes(_ string, entries eth69.NewBlockHashesPacket) {
	f.newBlockHashes = append(f.newBlockHashes, entries)
}
func (f *dispatchFakeResponseHandler) OnBlockAccessLists(_ string, _ uint64, bals [][]byte) {
	f.bals = append(f.bals, bals)
}

func dispatchTestPeer() *gethp2p.Peer {
	return gethp2p.NewPeer(enode.ID{}, "dispatch-test", nil)
}

func dispatchSendMessage(t *testing.T, h *EthHandler, code uint64, payload interface{}) error {
	t.Helper()
	encoded, err := rlp.EncodeToBytes(payload)
	if err != nil {
		t.Fatal(err)
	}
	msg := gethp2p.Msg{Code: code, Size: uint32(len(encoded)), Payload: bytes.NewReader(encoded)}
	return h.handleMessage(dispatchTestPeer(), nil, msg)
}

func TestHandleMessageNewBlockHashesRoutesToResponseHandler(t *testing.T) {
	rh := &dispatchFakeResponseHandler{}
	h := &EthHandler{rh: rh}
	packet := eth69.NewBlockHashesPacket{{Hash: types.HexToHash("0x01"), Number: 5}}
	if err := dispatchSendMessage(t, h, 1, &packet); err != nil {
		t.Fatal(err)
	}
	if len(rh.newBlockHashes) != 1 || len(rh.newBlockHashes[0]) != 1 {
		t.Fatalf("new block hashes not routed: %+v", rh.newBlockHashes)
	}
}

func TestHandleMessageBlockHeadersRoutesToResponseHandler(t *testing.T) {
	rh := &dispatchFakeResponseHandler{}
	h := &EthHandler{rh: rh}
	resp := blockHeadersPacket{RequestID: 7, Headers: []rlp.RawValue{{0xc0}}}
	if err := dispatchSendMessage(t, h, 4, &resp); err != nil {
		t.Fatal(err)
	}
	if len(rh.headers) != 1 || len(rh.headers[0]) != 1 {
		t.Fatalf("headers not routed: %+v", rh.headers)
	}
}

func TestHandleMessageBlockBodiesRoutesToResponseHandler(t *testing.T) {
	rh := &dispatchFakeResponseHandler{}
	h := &EthHandler{rh: rh}
	resp := blockBodiesPacket{RequestID: 1, Bodies: []BlockBody{{}}}
	if err := dispatchSendMessage(t, h, 6, &resp); err != nil {
		t.Fatal(err)
	}
	if len(rh.bodies) != 1 {
		t.Fatalf("bodies not routed: %+v", rh.bodies)
	}
}

func TestHandleMessageReceiptsRoutesToResponseHandler(t *testing.T) {
	rh := &dispatchFakeResponseHandler{}
	h := &EthHandler{rh: rh}
	resp := blockReceiptsPacket{RequestID: 3, Receipts: [][]rlp.RawValue{{{0xc0}}}}
	if err := dispatchSendMessage(t, h, 16, &resp); err != nil {
		t.Fatal(err)
	}
	if len(rh.receipts) != 1 {
		t.Fatalf("receipts not routed: %+v", rh.receipts)
	}
}

func TestHandleMessageBlockAccessListsRoutesToResponseHandler(t *testing.T) {
	rh := &dispatchFakeResponseHandler{}
	h := &EthHandler{rh: rh}
	resp := blockAccessListsPacket{RequestID: 2, BALs: [][]byte{{0x01}}}
	if err := dispatchSendMessage(t, h, eth69.BlockAccessListsMsg, &resp); err != nil {
		t.Fatal(err)
	}
	if len(rh.bals) != 1 {
		t.Fatalf("bals not routed: %+v", rh.bals)
	}
}

func TestHandleMessageNewBlockAndUnknownCodesAreIgnored(t *testing.T) {
	h := &EthHandler{}
	if err := dispatchSendMessage(t, h, 7, struct{}{}); err != nil {
		t.Fatalf("NewBlock (code 7): %v", err)
	}
	if err := dispatchSendMessage(t, h, 15, struct{}{}); err != nil {
		t.Fatalf("code 15: %v", err)
	}
	if err := dispatchSendMessage(t, h, 17, struct{}{}); err != nil {
		t.Fatalf("code 17 (range update): %v", err)
	}
	if err := dispatchSendMessage(t, h, 999, struct{}{}); err != nil {
		t.Fatalf("unknown code: %v", err)
	}
}

func TestHandleMessageDecodeErrorsAreSurfaced(t *testing.T) {
	h := &EthHandler{}
	// Code 1 expects an RLP list of NewBlockHashesEntry; a bare integer fails to decode.
	msg := gethp2p.Msg{Code: 1, Size: 1, Payload: bytes.NewReader([]byte{0x01})}
	if err := h.handleMessage(dispatchTestPeer(), nil, msg); err == nil {
		t.Fatal("expected decode error for malformed NewBlockHashes payload")
	}
}
