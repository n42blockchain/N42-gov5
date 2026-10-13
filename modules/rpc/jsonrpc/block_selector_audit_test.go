package jsonrpc

import (
	"encoding/json"
	"github.com/n42blockchain/N42/common/types"
	"strings"
	"testing"
)

func TestAuditBlockNumberOrHashStringPreservesHash(t *testing.T) {
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

func TestAuditBlockNumberOrHashStringAndObjectAgree(t *testing.T) {
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

func TestAuditHashUnmarshalTextWritesCaller(t *testing.T) {
	var hash types.Hash
	text := "0x" + strings.Repeat("12", 32)
	if err := UnmarshalText(&hash, []byte(text)); err != nil {
		t.Fatal(err)
	}
	if hash.Hex() != text {
		t.Fatalf("hash=%s", hash.Hex())
	}
}

func TestAuditBlockNumberOrHashRejectsMalformedHashString(t *testing.T) {
	var value BlockNumberOrHash
	if err := json.Unmarshal([]byte(`"0x`+strings.Repeat("gg", 32)+`"`), &value); err == nil {
		t.Fatal("invalid hexadecimal accepted")
	}
}

func TestAuditBlockNumberOrHashReusedDestination(t *testing.T) {
	hash := "0x" + strings.Repeat("ab", 32)
	var value BlockNumberOrHash
	for _, input := range []string{`{"blockHash":"` + hash + `","requireCanonical":true}`, `"latest"`, `"` + hash + `"`, `"0x2a"`} {
		if err := json.Unmarshal([]byte(input), &value); err != nil {
			t.Fatal(err)
		}
		if (value.BlockHash == nil) == (value.BlockNumber == nil) {
			t.Fatalf("selector retains both alternatives after %s", input)
		}
		if input[0] == '"' && value.RequireCanonical {
			t.Fatalf("scalar %s retains requireCanonical", input)
		}
	}
}

func TestAuditBlockNumberOrHashFinalityTags(t *testing.T) {
	for input, want := range map[string]BlockNumber{"safe": SafeBlockNumber, "finalized": FinalizedBlockNumber} {
		var value BlockNumberOrHash
		if err := json.Unmarshal([]byte(`"`+input+`"`), &value); err != nil {
			t.Fatal(err)
		}
		if got, ok := value.Number(); !ok || got != want {
			t.Fatalf("%s = %d, %v", input, got, ok)
		}
	}
}

func TestAuditBlockNumberOrHashEscapedScalars(t *testing.T) {
	for input, want := range map[string]BlockNumber{`"la\u0074est"`: LatestBlockNumber, `"0\u00782a"`: 42} {
		var value BlockNumberOrHash
		if err := json.Unmarshal([]byte(input), &value); err != nil {
			t.Fatal(err)
		}
		if got, ok := value.Number(); !ok || got != want {
			t.Fatalf("%s = %d, %v", input, got, ok)
		}
	}
}
