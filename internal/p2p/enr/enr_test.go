package enr

import (
	"errors"
	"testing"
)

func TestRecordSetSigRejectsNilSchemeWithSignature(t *testing.T) {
	var r Record
	err := r.SetSig(nil, []byte{0x01})
	if !errors.Is(err, errNilSchemeWithSig) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRecordSetSigRejectsNilSignatureWithScheme(t *testing.T) {
	var r Record
	err := r.SetSig(NullID{}, nil)
	if !errors.Is(err, errNilSigWithScheme) {
		t.Fatalf("unexpected error: %v", err)
	}
}

type NullID struct{}

func (NullID) Verify(*Record, []byte) error { return nil }
func (NullID) NodeAddr(*Record) []byte      { return nil }
