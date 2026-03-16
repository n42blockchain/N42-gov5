package jsonrpc

import (
	"encoding/json"
	"testing"
)

func TestParseMessageRejectsMalformedBatch(t *testing.T) {
	_, batch, err := parseMessage(json.RawMessage(`[{"jsonrpc":"2.0","id":1,"method":"eth_chainId"},]`))
	if !batch {
		t.Fatal("parseMessage() batch = false, want true")
	}
	if err == nil {
		t.Fatal("parseMessage() error = nil, want parse error for malformed batch")
	}
}

func TestParseMessageAcceptsEmptyBatch(t *testing.T) {
	msgs, batch, err := parseMessage(json.RawMessage(`[]`))
	if err != nil {
		t.Fatalf("parseMessage() error = %v", err)
	}
	if !batch {
		t.Fatal("parseMessage() batch = false, want true")
	}
	if len(msgs) != 0 {
		t.Fatalf("parseMessage() len(msgs) = %d, want 0", len(msgs))
	}
}

func TestParseMessageRejectsTrailingGarbageAfterBatch(t *testing.T) {
	_, batch, err := parseMessage(json.RawMessage(`[{"jsonrpc":"2.0","id":1,"method":"eth_chainId"}]{"jsonrpc":"2.0"}`))
	if !batch {
		t.Fatal("parseMessage() batch = false, want true")
	}
	if err == nil {
		t.Fatal("parseMessage() error = nil, want parse error for trailing data")
	}
}
