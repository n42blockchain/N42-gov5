package types

import "strings"

// EnforcePolicy preserves provider escalation and applies the request's gates.
// System1's third answer encodes that same escalation decision. Copy its slice
// before updating so a provider's reusable output cannot be mutated by callers.
func (r DecisionResult) EnforcePolicy(req DecisionRequest) DecisionResult {
	r.NeedEscalation = r.NeedEscalation || req.PolicyParameters.RequireHuman || r.ConfidencePPM < req.PolicyParameters.MinConfidencePPM || strings.EqualFold(r.Label, "UNKNOWN") || strings.EqualFold(r.Label, "ABSTAIN")
	if req.SchemaID == "system1-v1" && len(r.Answers) == 3 && r.Answers[2].Kind == 3 {
		r.NeedEscalation = r.NeedEscalation || r.Answers[2].ValuePPM > 0
		r.Answers = append([]QuantizedAnswer(nil), r.Answers...)
		r.Answers[2].ValuePPM = 0
		if r.NeedEscalation {
			r.Answers[2].ValuePPM = PPM
		}
	}
	return r
}
