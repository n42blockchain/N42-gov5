package sharedexec

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestResumePlanDocumentRejectsAliasesAndUnknownContent(t *testing.T) {
	p, manifest, baseline := resumeStructureFixture(t)
	id, err := ResumePlanID(p, manifest, baseline)
	if err != nil {
		t.Fatal(err)
	}
	document := ResumePlanDocument{Kind: "gov5-resume-four-frame-plan-v1", Plan: p, PlanID: id}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeResumePlanDocument(raw)
	if err != nil || !reflect.DeepEqual(document, got) {
		t.Fatal("valid offline document changed", err)
	}
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"unknown", append([]byte(`{"unknown":1,`), raw[1:]...)},
		{"case alias", append([]byte(`{"Kind":"different",`), raw[1:]...)},
		{"nested alias", bytes.Replace(raw, []byte(`"Identity":{`), []byte(`"Identity":{},"identity":{`), 1)},
		{"unicode alias", append([]byte(`{"kınd":"different",`), raw[1:]...)},
		{"trailing", append(append([]byte(nil), raw...), []byte(` {}`)...)},
		{"bad kind", bytes.Replace(raw, []byte(`gov5-resume-four-frame-plan-v1`), []byte(`other-kind`), 1)},
		{"null", []byte(`null`)},
		{"empty", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeResumePlanDocument(tc.raw)
			if err == nil || !reflect.DeepEqual(got, ResumePlanDocument{}) {
				t.Fatal("malformed document returned partial data", err)
			}
		})
	}
	// Parsing metadata does not authenticate source bytes or fresh state.
	document.Plan.Senders[0].Address[0]++
	changed, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := DecodeResumePlanDocument(changed)
	if err != nil {
		t.Fatal("decoder unexpectedly claimed to validate source", err)
	}
	if _, err := ResumePlanID(parsed.Plan, manifest, baseline); err == nil {
		t.Fatal("separate semantic validator accepted changed mapping")
	}
}
