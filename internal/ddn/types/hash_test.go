package types

import (
	"encoding/json"
	"os"
	"testing"

	chain "github.com/n42blockchain/N42/common/types"
)

type vector struct {
	Kind      string     `json:"kind"`
	Canonical string     `json:"canonical"`
	Hash      chain.Hash `json:"hash"`
}

func TestCanonicalVectors(t *testing.T) {
	b, err := os.ReadFile("testdata/canonical_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []vector
	if err = json.Unmarshal(b, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.Kind, func(t *testing.T) {
			var c []byte
			var h chain.Hash
			switch v.Kind {
			case "request":
				var r DecisionRequest
				err = json.Unmarshal([]byte(v.Canonical), &r)
				if err == nil {
					c, err = r.CanonicalBytes()
					h, _ = r.CanonicalHash()
					r.RequestID = h
					r.Signature = "0xdead"
					hh, _ := r.CanonicalHash()
					if hh != h {
						t.Fatal("circular request hash")
					}
				}
			case "receipt":
				var r DecisionReceipt
				err = json.Unmarshal([]byte(v.Canonical), &r)
				if err == nil {
					c, err = r.CanonicalBytes()
					h, _ = r.CanonicalHash()
					r.ProviderSignature = "0xdead"
					hh, _ := r.CanonicalHash()
					if hh != h {
						t.Fatal("signature altered receipt hash")
					}
				}
			case "manifest":
				var m ModelManifest
				err = json.Unmarshal([]byte(v.Canonical), &m)
				if err == nil {
					c, err = m.CanonicalBytes()
					h, _ = m.CanonicalHash()
				}
			default:
				t.Fatal("unknown vector")
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(c) != v.Canonical || h != v.Hash {
				t.Fatalf("canonical mismatch: %s %s", c, h)
			}
		})
	}
}
func TestCanonicalAndValidationBoundaries(t *testing.T) {
	r := DecisionRequest{Version: 1, Task: "node.anomaly", SchemaID: "health-v1", InputHash: chain.Hash{1}, Requester: "did:n42:test", PrivacyMode: "public", Quorum: 1, MaxLatencyMs: 100, MaxCost: "0", Deadline: 1000}
	if err := r.Validate(1); err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(1000); err == nil {
		t.Fatal("expired request accepted")
	}
	r.RequestID = chain.Hash{2}
	if err := r.Validate(1); err == nil {
		t.Fatal("substituted id accepted")
	}
	r.Task = string([]byte{255})
	if _, err := r.CanonicalBytes(); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	a := DecisionReceipt{Result: DecisionResult{Label: "<&>\u2028"}}
	b := a
	b.Result.ProbabilitiesPPM = []uint32{}
	b.Result.Answers = []QuantizedAnswer{}
	h, _ := a.CanonicalHash()
	hh, _ := b.CanonicalHash()
	if h != hh {
		t.Fatal("nil and empty arrays differ")
	}
	for _, result := range []DecisionResult{{Label: "x", ConfidencePPM: PPM + 1}, {Label: "x", ProbabilitiesPPM: []uint32{1}}, {Label: "x", Answers: []QuantizedAnswer{{Selected: 2, ProbabilitiesPPM: []uint32{PPM}}}}} {
		if result.Validate() == nil {
			t.Fatal("invalid result accepted")
		}
	}
}

func TestUnknownSignedFieldsRejected(t *testing.T) {
	for _, data := range []string{`{"unexpected":1}`, `{"policy_parameters":{"unknown":true}}`} {
		var r DecisionRequest
		if json.Unmarshal([]byte(data), &r) == nil {
			t.Fatal("unsigned field accepted")
		}
	}
}
