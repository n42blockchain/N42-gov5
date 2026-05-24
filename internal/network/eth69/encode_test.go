package eth69

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"
)

func TestGetBlockHeadersEncoding(t *testing.T) {
	req := GetBlockHeadersPacket{
		RequestID: 12345,
		Origin:    HashOrNumber{Number: 25101867},
		Amount:    192,
		Skip:      0,
		Reverse:   false,
	}
	var buf bytes.Buffer
	if err := rlp.Encode(&buf, &req); err != nil {
		t.Fatalf("encode: %v", err)
	}
	t.Logf("encoded %d bytes: %x", buf.Len(), buf.Bytes())
	fmt.Printf("encoded %d bytes: %x\n", buf.Len(), buf.Bytes())
}
