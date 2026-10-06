package aggregate

import (
	"testing"

	chain "github.com/n42blockchain/N42/common/types"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

func outcome(did, family, label string) Outcome {
	return Outcome{ProviderDID: did, ModelHash: chain.Hash{1}, Family: family, Result: d.DecisionResult{Label: label, ConfidencePPM: 900000, ProbabilitiesPPM: []uint32{900000, 100000}}}
}
func TestAgreementDisagreementMatrix(t *testing.T) {
	a, b, c := outcome("a", "rules", "NORMAL"), outcome("b", "gli", "NORMAL"), outcome("c", "other", "NETWORK")
	lower := b
	lower.Result.ConfidencePPM = 800000
	failed := b
	failed.Failed = true
	invalid := b
	invalid.Result.ProbabilitiesPPM = []uint32{1}
	correlated := b
	correlated.Family = a.Family
	uncertain := b
	uncertain.Result.NeedEscalation = true
	for _, tc := range []struct {
		name     string
		quorum   uint32
		out      []Outcome
		label    string
		escalate bool
	}{{"agreement", 2, []Outcome{a, b}, "NORMAL", false}, {"confidence minimum", 2, []Outcome{a, lower}, "NORMAL", false}, {"majority must escalate", 3, []Outcome{a, b, c}, "UNKNOWN", true}, {"partial failure", 2, []Outcome{a, failed}, "UNKNOWN", true}, {"missing", 2, []Outcome{a}, "UNKNOWN", true}, {"invalid", 2, []Outcome{a, invalid}, "UNKNOWN", true}, {"duplicate identity", 2, []Outcome{a, a}, "UNKNOWN", true}, {"correlated family", 2, []Outcome{a, correlated}, "UNKNOWN", true}, {"explicit escalation", 2, []Outcome{a, uncertain}, "NORMAL", true}, {"single provider", 1, []Outcome{a}, "NORMAL", false}, {"no response", 1, nil, "UNKNOWN", true}} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Combine(d.DecisionRequest{Quorum: tc.quorum}, tc.out)
			if err != nil {
				t.Fatal(err)
			}
			if r.Result.Label != tc.label || r.Result.NeedEscalation != tc.escalate || r.EvidenceCommitment == (chain.Hash{}) {
				t.Fatalf("unexpected aggregate %+v", r)
			}
			if tc.name == "confidence minimum" && r.Result.ConfidencePPM != 800000 {
				t.Fatal("confidence inflated")
			}
		})
	}
}
func TestTypedAnswerSplitAndPolicyEscalate(t *testing.T) {
	a, b := outcome("a", "rules", "NORMAL"), outcome("b", "gli", "NORMAL")
	a.Result.Answers = []d.QuantizedAnswer{{Kind: 1, Selected: 0, ProbabilitiesPPM: []uint32{500000, 500000}}}
	b.Result.Answers = []d.QuantizedAnswer{{Kind: 1, Selected: 1, ProbabilitiesPPM: []uint32{500000, 500000}}}
	r, _ := Combine(d.DecisionRequest{Quorum: 2}, []Outcome{a, b})
	if !r.Result.NeedEscalation || r.Result.Label != "UNKNOWN" {
		t.Fatal("typed answer disagreement suppressed")
	}
	a.Result.Answers = nil
	b.Result.Answers = nil
	r, _ = Combine(d.DecisionRequest{Quorum: 2, PolicyParameters: d.PolicyParameters{RequireHuman: true}}, []Outcome{a, b})
	if !r.Result.NeedEscalation {
		t.Fatal("human policy suppressed")
	}
}
