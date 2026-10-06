package types

import "errors"

// QuestionPolicy matches DecisionHub template bounds and review thresholds.
type QuestionPolicy struct {
	Kind              uint8  `json:"kind"`
	Size              uint8  `json:"size"`
	MinProbabilityPPM uint32 `json:"min_probability_ppm"`
	MinConfidencePPM  uint32 `json:"min_confidence_ppm"`
	ReviewOption      uint8  `json:"review_option"`
}

// ValidateGovernanceAnswers validates a frozen template and returns its review
// decision. The caller must bind the template hash through the signed policy.
func ValidateGovernanceAnswers(policy []QuestionPolicy, answers []QuantizedAnswer) (bool, error) {
	if len(policy) < 1 || len(policy) > 8 || len(policy) != len(answers) {
		return false, errors.New("invalid governance question count")
	}
	review := false
	for i, p := range policy {
		if p.MinProbabilityPPM > PPM || p.MinConfidencePPM > PPM {
			return false, errors.New("invalid governance threshold")
		}
		switch p.Kind {
		case 1:
			if p.Size < 2 || p.Size > 16 || (p.ReviewOption != 255 && p.ReviewOption >= p.Size) {
				return false, errors.New("invalid choice policy")
			}
		case 2:
			if p.Size < 2 || p.Size > 10 || p.ReviewOption != 255 {
				return false, errors.New("invalid score policy")
			}
		case 3:
			if p.Size != 0 || p.ReviewOption != 255 {
				return false, errors.New("invalid Noul policy")
			}
		default:
			return false, errors.New("invalid governance kind")
		}
		a := answers[i]
		if err := a.Validate(); err != nil {
			return false, err
		}
		if a.Kind != p.Kind || len(a.ProbabilitiesPPM) != int(p.Size) {
			return false, errors.New("answer does not match template")
		}
		if a.Kind == 3 {
			review = review || a.ValuePPM < p.MinProbabilityPPM
		} else {
			review = review || a.ConfidencePPM < p.MinConfidencePPM
			if a.Kind == 1 {
				review = review || a.ProbabilitiesPPM[a.Selected] < p.MinProbabilityPPM || a.Selected == p.ReviewOption
			}
		}
	}
	return review, nil
}
