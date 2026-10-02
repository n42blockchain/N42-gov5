package devp2p

import (
	"bytes"
	"testing"

	gethp2p "github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"
)

func TestNewSnapHandlerReturnsHandler(t *testing.T) {
	if NewSnapHandler() == nil {
		t.Fatal("NewSnapHandler() returned nil")
	}
}

// snapSendAndRead encodes req, feeds it through h.handle on one end of a
// MsgPipe, and returns the decoded response (or the handle error).
func snapSendAndRead(t *testing.T, code uint64, req interface{}, resp interface{}) error {
	t.Helper()
	h := &SnapHandler{}
	encoded, err := rlp.EncodeToBytes(req)
	if err != nil {
		t.Fatal(err)
	}
	msg := gethp2p.Msg{Code: code, Size: uint32(len(encoded)), Payload: bytes.NewReader(encoded)}
	rw1, rw2 := gethp2p.MsgPipe()
	defer rw1.Close()
	defer rw2.Close()
	errCh := make(chan error, 1)
	go func() { errCh <- h.handle("peer", rw1, msg) }()

	respMsg, rerr := rw2.ReadMsg()
	if rerr != nil {
		t.Fatal(rerr)
	}
	if resp != nil {
		if derr := respMsg.Decode(resp); derr != nil {
			t.Fatal(derr)
		}
	}
	return <-errCh
}

func TestSnapHandleGetAccountRangeEchoesID(t *testing.T) {
	var resp snapAccountRangePacket
	if err := snapSendAndRead(t, snapGetAccountRangeMsg, &snapGetAccountRangePacket{ID: 11}, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID != 11 || len(resp.Accounts) != 0 {
		t.Fatalf("resp = %+v, want ID=11 empty accounts", resp)
	}
}

func TestSnapHandleGetStorageRangesEchoesID(t *testing.T) {
	var resp snapStorageRangesPacket
	if err := snapSendAndRead(t, snapGetStorageRangesMsg, &snapGetStorageRangesPacket{ID: 22}, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID != 22 || len(resp.Slots) != 0 {
		t.Fatalf("resp = %+v, want ID=22 empty slots", resp)
	}
}

func TestSnapHandleGetByteCodesEchoesID(t *testing.T) {
	var resp snapByteCodesPacket
	if err := snapSendAndRead(t, snapGetByteCodesMsg, &snapGetByteCodesPacket{ID: 33}, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID != 33 || len(resp.Codes) != 0 {
		t.Fatalf("resp = %+v, want ID=33 empty codes", resp)
	}
}

func TestSnapHandleGetTrieNodesEchoesID(t *testing.T) {
	var resp snapTrieNodesPacket
	if err := snapSendAndRead(t, snapGetTrieNodesMsg, &snapGetTrieNodesPacket{ID: 44}, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID != 44 || len(resp.Nodes) != 0 {
		t.Fatalf("resp = %+v, want ID=44 empty nodes", resp)
	}
}

func TestSnapHandleResponseCodesAreIgnored(t *testing.T) {
	h := &SnapHandler{}
	for _, code := range []uint64{snapAccountRangeMsg, snapStorageRangesMsg, snapByteCodesMsg, snapTrieNodesMsg, 0xff} {
		encoded, err := rlp.EncodeToBytes(&snapAccountRangePacket{ID: 1})
		if err != nil {
			t.Fatal(err)
		}
		msg := gethp2p.Msg{Code: code, Size: uint32(len(encoded)), Payload: bytes.NewReader(encoded)}
		if err := h.handle("peer", nil, msg); err != nil {
			t.Fatalf("code %d: unexpected error %v", code, err)
		}
	}
}

func TestSnapHandleDecodeErrorIsSurfaced(t *testing.T) {
	h := &SnapHandler{}
	// A bare integer is not a valid snapGetAccountRangePacket struct.
	msg := gethp2p.Msg{Code: snapGetAccountRangeMsg, Size: 1, Payload: bytes.NewReader([]byte{0x01})}
	if err := h.handle("peer", nil, msg); err == nil {
		t.Fatal("expected decode error for malformed GetAccountRange payload")
	}
}
