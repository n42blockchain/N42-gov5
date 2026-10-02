package avmtypes

import (
	"encoding/json"
	"testing"

	"github.com/n42blockchain/N42/common/avmutil"
)

func sampleLog() Log {
	return Log{
		Address:     avmutil.BytesToAddress([]byte{0x01}),
		Topics:      []avmutil.Hash{avmutil.BytesToHash([]byte{0x02})},
		Data:        []byte{0xDE, 0xAD},
		BlockNumber: 42,
		TxHash:      avmutil.BytesToHash([]byte{0x03}),
		TxIndex:     1,
		BlockHash:   avmutil.BytesToHash([]byte{0x04}),
		Index:       2,
		Removed:     true,
	}
}

func TestLogMarshalUnmarshalJSONRoundTrip(t *testing.T) {
	l := sampleLog()
	data, err := json.Marshal(l)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var got Log
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if got.Address != l.Address || got.BlockNumber != l.BlockNumber || got.TxIndex != l.TxIndex ||
		got.Index != l.Index || got.Removed != l.Removed || string(got.Data) != string(l.Data) {
		t.Fatalf("round-trip mismatch: got %#v, want %#v", got, l)
	}
}

func TestLogUnmarshalJSONMissingRequiredFields(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"missing address", `{"topics":[],"data":"0x"}`},
		{"missing topics", `{"address":"0x0000000000000000000000000000000000000001","data":"0x"}`},
		{"missing data", `{"address":"0x0000000000000000000000000000000000000001","topics":[]}`},
		{"missing txHash", `{"address":"0x0000000000000000000000000000000000000001","topics":[],"data":"0x"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var l Log
			if err := json.Unmarshal([]byte(c.json), &l); err == nil {
				t.Fatalf("expected error for %s", c.name)
			}
		})
	}
}

func TestLogUnmarshalJSONOptionalFieldsDefault(t *testing.T) {
	// A minimal but complete-required-fields payload; optional fields
	// (blockNumber, txIndex, blockHash, logIndex, removed) are omitted and
	// should remain at their zero values rather than erroring.
	raw := `{
		"address":"0x0000000000000000000000000000000000000001",
		"topics":["0x0000000000000000000000000000000000000000000000000000000000000002"],
		"data":"0xdead",
		"transactionHash":"0x0000000000000000000000000000000000000000000000000000000000000003"
	}`
	var l Log
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if l.BlockNumber != 0 || l.TxIndex != 0 || l.Index != 0 || l.Removed {
		t.Fatalf("expected zero-valued optional fields, got %#v", l)
	}
}

func TestLogUnmarshalJSONInvalid(t *testing.T) {
	var l Log
	if err := json.Unmarshal([]byte("not json"), &l); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
