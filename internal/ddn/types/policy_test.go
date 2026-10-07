package types

import "testing"

func TestSystem1PolicySynchronizesEscalationAnswer(t *testing.T) {
	original := DecisionResult{Label: "NORMAL", Answers: []QuantizedAnswer{{Kind: 1, ProbabilitiesPPM: []uint32{PPM, 0, 0}}, {Kind: 1, ProbabilitiesPPM: []uint32{PPM, 0, 0, 0, 0, 0, 0, 0}}, {Kind: 3}}}
	for _, policy := range []PolicyParameters{{RequireHuman: true}, {MinConfidencePPM: 1}} {
		result := original.EnforcePolicy(DecisionRequest{SchemaID: "system1-v1", PolicyParameters: policy})
		if !result.NeedEscalation || result.Answers[2].ValuePPM != PPM {
			t.Fatal("policy escalation contradicts signed answer")
		}
		if err := result.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	if original.Answers[2].ValuePPM != 0 {
		t.Fatal("policy mutated shared provider output")
	}
	generic := original.EnforcePolicy(DecisionRequest{SchemaID: "governance", PolicyParameters: PolicyParameters{RequireHuman: true}})
	if generic.Answers[2].ValuePPM != 0 {
		t.Fatal("rewrote unrelated governance Noul value")
	}
}

func TestSystem1PreservesTypedEscalation(t *testing.T) {
	r := DecisionResult{Label: "NORMAL", Answers: []QuantizedAnswer{{Kind: 1}, {Kind: 1}, {Kind: 3, ValuePPM: PPM}}}
	r = r.EnforcePolicy(DecisionRequest{SchemaID: "system1-v1"})
	if !r.NeedEscalation || r.Answers[2].ValuePPM != PPM {
		t.Fatal("typed escalation was downgraded")
	}
}

func TestSystem1RejectsMislabeledOrMalformedSignedAnswers(t *testing.T) {
	r := DecisionResult{Label: "NORMAL", ProbabilitiesPPM: []uint32{PPM, 0, 0, 0, 0, 0, 0, 0}, Answers: []QuantizedAnswer{{Kind: 1, ProbabilitiesPPM: []uint32{PPM, 0, 0}}, {Kind: 1, ProbabilitiesPPM: []uint32{PPM, 0, 0, 0, 0, 0, 0, 0}}, {Kind: 3}}}
	if err := r.ValidateSchema("system1-v1"); err != nil {
		t.Fatal(err)
	}
	bad := r
	bad.Label = "NETWORK"
	if bad.ValidateSchema("system1-v1") == nil {
		t.Fatal("mislabeled choice accepted")
	}
	bad = r.EnforcePolicy(DecisionRequest{SchemaID: "system1-v1", PolicyParameters: PolicyParameters{RequireHuman: true}})
	bad.Answers[2].ValuePPM = 0
	if bad.ValidateSchema("system1-v1") == nil {
		t.Fatal("contradictory escalation accepted")
	}
	bad = r
	bad.Answers = nil
	if bad.ValidateSchema("system1-v1") == nil {
		t.Fatal("missing typed answers accepted")
	}
	if err := (DecisionResult{Label: "UNKNOWN", NeedEscalation: true}).ValidateSchema("system1-v1"); err != nil {
		t.Fatal(err)
	}
	bad = r
	bad.Answers = append([]QuantizedAnswer(nil), r.Answers...)
	bad.Answers[0] = QuantizedAnswer{Kind: 1, Selected: 2, ProbabilitiesPPM: []uint32{0, 0, PPM}}
	if bad.ValidateSchema("system1-v1") == nil {
		t.Fatal("critical health without escalation accepted")
	}
}
