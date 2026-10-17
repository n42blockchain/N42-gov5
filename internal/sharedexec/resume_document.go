package sharedexec

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/n42blockchain/N42/common/types"
)

const ResumeMaxPlanDocumentBytes = 16 << 20

// ResumePlanDocument is offline content. Decoding it grants no owner capability;
// local source verification and a fresh canonical vector remain mandatory.
type ResumePlanDocument struct {
	Kind   string     `json:"kind"`
	Plan   ResumePlan `json:"plan"`
	PlanID types.Hash `json:"plan_id"`
}

func DecodeResumePlanDocument(raw []byte) (ResumePlanDocument, error) {
	var out ResumePlanDocument
	if len(raw) == 0 || len(raw) > ResumeMaxPlanDocumentBytes {
		return out, errors.New("resume plan document exceeds bounds")
	}
	if err := decodeResumePlanJSON(raw, &out); err != nil {
		return ResumePlanDocument{}, err
	}
	if out.Kind != "gov5-resume-four-frame-plan-v1" || out.PlanID == (types.Hash{}) || len(out.Plan.Senders) < 1 || len(out.Plan.Senders) > 12500 || len(out.Plan.Frames) != 4 {
		return ResumePlanDocument{}, errors.New("invalid bounded resume plan document")
	}
	return out, nil
}

func decodeResumePlanJSON(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return errors.New("resume JSON nesting exceeds limit")
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		delim, nested := t.(json.Delim)
		if !nested {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]bool)
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || strings.IndexFunc(name, func(r rune) bool { return r > 127 }) >= 0 {
					return errors.New("resume JSON requires ASCII object keys")
				}
				// encoding/json matches struct fields case-insensitively.
				// Reject aliases as well as exact duplicate spellings.
				folded := strings.ToLower(name)
				if seen[folded] {
					return errors.New("resume JSON duplicate/invalid object key")
				}
				seen[folded] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid resume JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("one resume JSON value required")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(out)
}
