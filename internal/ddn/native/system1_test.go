package native

import (
	"context"
	"testing"
)

func TestSystem1ReferenceCases(t *testing.T) {
	cases := []struct {
		text, label string
		escalation  bool
		severity    uint8
	}{
		{"qc mismatch", "CONSENSUS", true, 2},
		{"data unavailable", "NORMAL", true, 2},
		{"consensus halted", "CONSENSUS", true, 2},
		{"missing env", "CONFIGURATION", false, 0},
		{"tps regression", "PERFORMANCE", false, 1},
		{"healthy peers connected", "NETWORK", false, 0},
		{"finality stalled", "CONSENSUS", true, 2},
		{"unexpected panic", "UNKNOWN", true, 0},
	}
	for _, c := range cases {
		r, err := System1(context.Background(), c.text)
		if err != nil {
			t.Fatal(err)
		}
		if r.Label != c.label || r.NeedEscalation != c.escalation || r.Answers[0].Selected != c.severity {
			t.Fatalf("%s: %+v", c.text, r)
		}
		if err = r.ValidateSchema("system1-v1"); err != nil {
			t.Fatal(err)
		}
	}
}
