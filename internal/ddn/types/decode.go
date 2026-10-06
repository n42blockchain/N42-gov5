package types

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// Strict decoding avoids accepting fields that are absent from signed bytes.
func strictDecode(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing DDN JSON")
	}
	return nil
}
func (r *DecisionRequest) UnmarshalJSON(data []byte) error {
	type wire DecisionRequest
	var v wire
	if err := strictDecode(data, &v); err != nil {
		return err
	}
	*r = DecisionRequest(v)
	return nil
}
func (r *DecisionReceipt) UnmarshalJSON(data []byte) error {
	type wire DecisionReceipt
	var v wire
	if err := strictDecode(data, &v); err != nil {
		return err
	}
	*r = DecisionReceipt(v)
	return nil
}
func (m *ModelManifest) UnmarshalJSON(data []byte) error {
	type wire ModelManifest
	var v wire
	if err := strictDecode(data, &v); err != nil {
		return err
	}
	*m = ModelManifest(v)
	return nil
}
