package sharedexec

import (
	"errors"

	"github.com/n42blockchain/N42/common/types"
)

// Decoding offline content does not verify source inclusion or arm any owner.
type BoundedResumePlanDocument struct {
	Kind   string            `json:"kind"`
	Plan   BoundedResumePlan `json:"plan"`
	PlanID types.Hash        `json:"plan_id"`
}

func DecodeBoundedResumePlanDocument(raw []byte) (BoundedResumePlanDocument, error) {
	var out BoundedResumePlanDocument
	if len(raw) == 0 || len(raw) > ResumeMaxPlanDocumentBytes {
		return out, errors.New("bounded resume plan document exceeds bounds")
	}
	if err := decodeResumePlanJSON(raw, &out); err != nil {
		return BoundedResumePlanDocument{}, err
	}
	if out.Kind != "gov5-bounded-resume-prefix-plan-v2" || out.Plan.Version != 2 || out.PlanID == (types.Hash{}) || len(out.Plan.Senders) < 1 || len(out.Plan.Senders) > 12500 {
		return BoundedResumePlanDocument{}, errors.New("invalid explicit bounded resume plan document")
	}
	return out, nil
}
