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
