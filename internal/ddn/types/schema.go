package types

import "errors"

// System1Labels returns the category order committed by system1-v1 answers.
func System1Labels() []string {
	return []string{"NORMAL", "NETWORK", "CONSENSUS", "EXECUTION", "STORAGE", "CONFIGURATION", "PERFORMANCE", "UNKNOWN"}
}

// ValidateSchema applies known wire-schema invariants as well as tuple bounds.
// Unknown application schemas retain generic validation; their templates must
// be authenticated separately. UNKNOWN without answers is explicit abstention.
func (r DecisionResult) ValidateSchema(schema string) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if schema != "system1-v1" {
		return nil
	}
	if r.Label == "UNKNOWN" && r.NeedEscalation && r.ConfidencePPM == 0 && len(r.Answers) == 0 && len(r.ProbabilitiesPPM) == 0 {
		return nil
	}
	if len(r.Answers) != 3 || r.Answers[0].Kind != 1 || len(r.Answers[0].ProbabilitiesPPM) != 3 || r.Answers[1].Kind != 1 || len(r.Answers[1].ProbabilitiesPPM) != 8 || r.Answers[2].Kind != 3 || len(r.ProbabilitiesPPM) != 8 {
		return errors.New("invalid System1 answer layout")
	}
	labels := System1Labels()
	category := r.Answers[1]
	if r.Label != labels[category.Selected] {
		return errors.New("System1 category label mismatch")
	}
	for i, p := range r.ProbabilitiesPPM {
		if p != category.ProbabilitiesPPM[i] {
			return errors.New("System1 category distribution mismatch")
		}
	}
	escalation := r.Answers[2].ValuePPM
	if (escalation != 0 && escalation != PPM) || r.NeedEscalation != (escalation == PPM) || ((r.Answers[0].Selected == 2 || r.Label == "UNKNOWN") && !r.NeedEscalation) {
		return errors.New("inconsistent System1 escalation")
	}
	return nil
}
