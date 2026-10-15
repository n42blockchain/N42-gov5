package sharedvalidator

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/internal/sharedexec"
)

func TestWireTransactionsRejectsOversizedArraysAndInvalidElements(t *testing.T) {
	var wire WireTransactions
	if err := json.Unmarshal([]byte(`["AQ==","Ag=="]`), &wire); err != nil || len(wire) != 2 || wire[1][0] != 2 {
		t.Fatalf("wire decode: %v", err)
	}
	for _, raw := range []string{`null`, `{}`, `[null]`, `[""]`, `["not base64!"]`, `["AQ=="] {}`} {
		if err := json.Unmarshal([]byte(raw), &wire); err == nil {
			t.Fatalf("invalid wire array accepted: %s", raw)
		}
	}
	// Small wire entries exercise the element cap during decoding without
	// requiring a maximum-sized block or allocating a huge decoded array.
	oversized := "[" + strings.Repeat(`"AQ==",`, sharedexec.MaxTransactions) + `"AQ=="]`
	if err := json.Unmarshal([]byte(oversized), &wire); err == nil {
		t.Fatal("transaction count above wire budget accepted")
	}
}
