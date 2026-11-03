package types

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"unicode/utf8"

	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// canonical uses struct declaration order, Go JSON escaping, compact UTF-8,
// no trailing newline, and empty arrays instead of null. No maps/raw JSON
// or floats are permitted in this version's signed data.
func canonical(v any) ([]byte, error) {
	if !validStrings(reflect.ValueOf(v)) {
		return nil, errors.New("canonical JSON requires valid UTF-8")
	}
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'}), nil
}
func hash(v any) (chain.Hash, error) {
	b, err := canonical(v)
	if err != nil {
		return chain.Hash{}, err
	}
	return crypto.Keccak256Hash(b), nil
}

// RequestID and signatures are blanked to prevent circular hashing.
func (r DecisionRequest) CanonicalBytes() ([]byte, error) {
	r.RequestID = chain.Hash{}
	r.Signature = ""
	return canonical(r)
}
func (r DecisionRequest) CanonicalHash() (chain.Hash, error) {
	b, err := r.CanonicalBytes()
	if err != nil {
		return chain.Hash{}, err
	}
	return crypto.Keccak256Hash(b), nil
}
func (r DecisionReceipt) CanonicalBytes() ([]byte, error) {
	r.ProviderSignature = ""
	r.Result = normalizeResult(r.Result)
	return canonical(r)
}
func (r DecisionReceipt) CanonicalHash() (chain.Hash, error) {
	b, err := r.CanonicalBytes()
	if err != nil {
		return chain.Hash{}, err
	}
	return crypto.Keccak256Hash(b), nil
}
func (m ModelManifest) CanonicalBytes() ([]byte, error) {
	m.Signature = ""
	if m.SupportedTasks == nil {
		m.SupportedTasks = []string{}
	}
	if m.SupportedSchemas == nil {
		m.SupportedSchemas = []string{}
	}
	return canonical(m)
}
func (m ModelManifest) CanonicalHash() (chain.Hash, error) {
	b, err := m.CanonicalBytes()
	if err != nil {
		return chain.Hash{}, err
	}
	return crypto.Keccak256Hash(b), nil
}
func normalizeResult(r DecisionResult) DecisionResult {
	if r.ProbabilitiesPPM == nil {
		r.ProbabilitiesPPM = []uint32{}
	}
	r.Answers = append([]QuantizedAnswer{}, r.Answers...)
	for i := range r.Answers {
		if r.Answers[i].ProbabilitiesPPM == nil {
			r.Answers[i].ProbabilitiesPPM = []uint32{}
		}
	}
	return r
}

func validStrings(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		return utf8.ValidString(v.String())
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if !validStrings(v.Field(i)) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if !validStrings(v.Index(i)) {
				return false
			}
		}
	}
	return true
}
