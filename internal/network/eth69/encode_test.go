package eth69

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"
)

// TestGetBlockHeadersEncoding pins the wire-form bytes so any future
// schema regression (e.g. flattening the inner query back out) trips
// here long before it trips a real geth peer's Disconnect frame.
func TestGetBlockHeadersEncoding(t *testing.T) {
	req := GetBlockHeadersPacket{
		RequestID: 12345,
		GetBlockHeadersQuery: &GetBlockHeadersQuery{
			Origin:  HashOrNumber{Number: 25101867},
			Amount:  192,
			Skip:    0,
			Reverse: false,
		},
	}
	var buf bytes.Buffer
	if err := rlp.Encode(&buf, &req); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got := buf.Bytes()
	t.Logf("encoded %d bytes: %x", buf.Len(), got)
	// Expected wire form: list( reqID, list( number, amount, skip, reverse ) )
	// Decoding the prefix:
	//   c? = outer list header
	//   82 30 39 = reqID 12345 (2-byte string)
	//   c? = inner list header
	//   84 01 7f 06 2b = number 25101867 (4-byte string — encoded by
	//                    our HashOrNumber.EncodeRLP)
	//   81 c0 = amount 192
	//   80 = skip 0
	//   80 = reverse false
	want := []byte{
		0xcd, // outer list, length 13
		0x82, 0x30, 0x39, // reqID 12345 = 0x3039
		0xc9,                         // inner list, length 9
		0x84, 0x01, 0x7f, 0x06, 0x2b, // number 25101867
		0x81, 0xc0, // amount 192
		0x80, // skip 0
		0x80, // reverse false
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("wire mismatch.\ngot:  %x\nwant: %x", got, want)
	}
}
