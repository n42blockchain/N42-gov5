package jsonrpc

import (
	"encoding/json"
	"github.com/n42blockchain/N42/common/types"
	"strings"
	"testing"
)

func TestBlockNumberOrHashStringPreservesHash(t *testing.T) {
	text := "0x" + strings.Repeat("a1", 32)
	var value BlockNumberOrHash
	if err := json.Unmarshal([]byte(`"`+text+`"`), &value); err != nil {
		t.Fatal(err)
	}
	got, ok := value.Hash()
	if !ok || got.Hex() != text {
		t.Fatalf("hash=%s ok=%v want=%s", got.Hex(), ok, text)
	}
	if _, ok := value.Number(); ok {
		t.Fatal("hash interpreted as number")
	}
}

func TestBlockNumberOrHashStringAndObjectAgree(t *testing.T) {
	text := "0x" + strings.Repeat("7f", 32)
	var scalar, object BlockNumberOrHash
	if err := json.Unmarshal([]byte(`"`+text+`"`), &scalar); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"blockHash":"`+text+`","requireCanonical":true}`), &object); err != nil {
		t.Fatal(err)
	}
	a, okA := scalar.Hash()
	b, okB := object.Hash()
	if !okA || !okB || a != b || !object.RequireCanonical {
		t.Fatal("scalar/object hash mismatch")
	}
}

func TestHashUnmarshalTextWritesCaller(t *testing.T) {
	var hash types.Hash
	text := "0x" + strings.Repeat("12", 32)
	if err := UnmarshalText(&hash, []byte(text)); err != nil {
		t.Fatal(err)
	}
	if hash.Hex() != text {
		t.Fatalf("hash=%s", hash.Hex())
	}
}

func TestBlockNumberOrHashRejectsMalformedHashString(t *testing.T) {
	var value BlockNumberOrHash
	if err := json.Unmarshal([]byte(`"0x`+strings.Repeat("gg", 32)+`"`), &value); err == nil {
		t.Fatal("invalid hexadecimal accepted")
	}
}
