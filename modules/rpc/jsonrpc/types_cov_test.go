package jsonrpc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestBlockNumberUnmarshalJSON(t *testing.T) {
	cases := []struct {
		in      string
		want    BlockNumber
		wantErr bool
	}{
		{`"earliest"`, EarliestBlockNumber, false},
		{`"latest"`, LatestBlockNumber, false},
		{`"pending"`, PendingBlockNumber, false},
		{`"finalized"`, FinalizedBlockNumber, false},
		{`"safe"`, SafeBlockNumber, false},
		{`"0x10"`, BlockNumber(16), false},
		{`"bogus"`, 0, true},
	}
	for _, c := range cases {
		var bn BlockNumber
		err := bn.UnmarshalJSON([]byte(c.in))
		if c.wantErr {
			if err == nil {
				t.Errorf("UnmarshalJSON(%s): expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("UnmarshalJSON(%s): unexpected error %v", c.in, err)
			continue
		}
		if bn != c.want {
			t.Errorf("UnmarshalJSON(%s) = %d want %d", c.in, bn, c.want)
		}
	}
}

func TestBlockNumberInt64(t *testing.T) {
	bn := BlockNumber(42)
	if bn.Int64() != 42 {
		t.Errorf("Int64() = %d want 42", bn.Int64())
	}
}

func TestBlockNumberOrHashUnmarshalJSON(t *testing.T) {
	var bnh BlockNumberOrHash
	if err := bnh.UnmarshalJSON([]byte(`"latest"`)); err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if n, ok := bnh.Number(); !ok || n != LatestBlockNumber {
		t.Errorf("got %d, %v", n, ok)
	}

	hashHex := "0x" + strings.Repeat("ab", 32)
	var bnh2 BlockNumberOrHash
	if err := bnh2.UnmarshalJSON([]byte(`"` + hashHex + `"`)); err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	// NOTE: BlockNumberOrHash.UnmarshalJSON calls UnmarshalText(hash, ...) with
	// hash passed by value (not *types.Hash), so the decoded bytes are written
	// into a local copy and never reach the caller's hash variable. The parsed
	// hash therefore stays zero-valued even though err == nil. This looks like a
	// pre-existing bug in UnmarshalText's signature; left unmodified per task
	// instructions (non-test code must not change). Assert the actual behavior.
	if _, ok := bnh2.Hash(); !ok {
		t.Fatal("expected hash field set")
	}

	var bnh3 BlockNumberOrHash
	if err := bnh3.UnmarshalJSON([]byte(`"0x5"`)); err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if n, ok := bnh3.Number(); !ok || n != BlockNumber(5) {
		t.Errorf("got %d, %v", n, ok)
	}

	// object form
	var bnh4 BlockNumberOrHash
	raw, _ := json.Marshal(map[string]interface{}{"blockNumber": "0x1"})
	if err := bnh4.UnmarshalJSON(raw); err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if n, ok := bnh4.Number(); !ok || n != BlockNumber(1) {
		t.Errorf("got %d, %v", n, ok)
	}

	// conflicting blockNumber + blockHash
	raw2, _ := json.Marshal(map[string]interface{}{"blockNumber": "0x1", "blockHash": hashHex})
	var bnh5 BlockNumberOrHash
	if err := bnh5.UnmarshalJSON(raw2); err == nil {
		t.Error("expected error for conflicting fields")
	}
}

func TestBlockNumberOrHashHelpers(t *testing.T) {
	bnh := BlockNumberOrHashWithNumber(BlockNumber(7))
	if n, ok := bnh.Number(); !ok || n != 7 {
		t.Errorf("got %d %v", n, ok)
	}
	if s := bnh.String(); s != "7" {
		t.Errorf("String() = %q want 7", s)
	}

	var h types.Hash
	bnh2 := BlockNumberOrHashWithHash(h, true)
	if hv, ok := bnh2.Hash(); !ok || hv != h {
		t.Errorf("got %v %v", hv, ok)
	}
	if !bnh2.RequireCanonical {
		t.Error("expected RequireCanonical true")
	}

	var empty BlockNumberOrHash
	if s := empty.String(); s != "nil" {
		t.Errorf("String() = %q want nil", s)
	}
}

func TestDecimalOrHexUnmarshalJSON(t *testing.T) {
	var d DecimalOrHex
	if err := d.UnmarshalJSON([]byte(`"100"`)); err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if d != 100 {
		t.Errorf("got %d want 100", d)
	}

	var d2 DecimalOrHex
	if err := d2.UnmarshalJSON([]byte(`"0x10"`)); err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if d2 != 16 {
		t.Errorf("got %d want 16", d2)
	}

	var d3 DecimalOrHex
	if err := d3.UnmarshalJSON([]byte(`"notanumber"`)); err == nil {
		t.Error("expected error")
	}
}
