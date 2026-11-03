package types

import "testing"

func TestGovernanceAnswerCompatibility(t *testing.T) {
	valid := []QuantizedAnswer{
		{Kind: 3, ValuePPM: 800000},
		{Kind: 2, ValuePPM: 3000000, ProbabilitiesPPM: []uint32{0, 0, 0, 1000000}},
		{Kind: 1, Selected: 1, ProbabilitiesPPM: []uint32{333333, 666666}},
	}
	for _, a := range valid {
		if err := a.Validate(); err != nil {
			t.Fatalf("valid Rust tuple %+v: %v", a, err)
		}
	}
	invalid := []QuantizedAnswer{
		{Kind: 0}, {Kind: 3, ConfidencePPM: 1},
		{Kind: 1, ProbabilitiesPPM: []uint32{333333, 666666}},
		{Kind: 1, Selected: 1, ValuePPM: 1, ProbabilitiesPPM: []uint32{0, 1000000}},
		{Kind: 2, Selected: 1, ProbabilitiesPPM: []uint32{0, 1000000}},
		{Kind: 2, ValuePPM: 1000001, ProbabilitiesPPM: []uint32{0, 1000000}},
		{Kind: 1, ProbabilitiesPPM: []uint32{500000, 499997}},
	}
	for _, a := range invalid {
		if a.Validate() == nil {
			t.Fatalf("accepted invalid tuple %+v", a)
		}
	}
}

func TestGovernanceReviewPolicy(t *testing.T) {
	p := []QuestionPolicy{{Kind: 1, Size: 2, MinConfidencePPM: 700000, ReviewOption: 1}, {Kind: 3, ReviewOption: 255, MinProbabilityPPM: 500000}}
	a := []QuantizedAnswer{{Kind: 1, ConfidencePPM: 800000, ProbabilitiesPPM: []uint32{800000, 200000}}, {Kind: 3, ValuePPM: 800000}}
	review, err := ValidateGovernanceAnswers(p, a)
	if err != nil || review {
		t.Fatalf("%v %v", review, err)
	}
	a[1].ValuePPM = 400000
	review, err = ValidateGovernanceAnswers(p, a)
	if err != nil || !review {
		t.Fatalf("%v %v", review, err)
	}
	p[0].Size = 17
	if _, err = ValidateGovernanceAnswers(p, a); err == nil {
		t.Fatal("accepted contract-invalid template")
	}
}
