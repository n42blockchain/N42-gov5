package abi

import (
	"math/big"
	"strings"
	"testing"
)

const lookupABI = `[
	{"type":"function","name":"foo","inputs":[{"name":"a","type":"uint256"}],"outputs":[{"name":"b","type":"uint256"}],"stateMutability":"view"},
	{"type":"function","name":"bar","inputs":[{"name":"x","type":"string"}],"outputs":[],"stateMutability":"payable"},
	{"type":"event","name":"Transfer","inputs":[{"name":"from","type":"address","indexed":true},{"name":"to","type":"address","indexed":true},{"name":"value","type":"uint256","indexed":false}],"anonymous":false},
	{"type":"error","name":"CustomError","inputs":[{"name":"code","type":"uint256"}]}
]`

func parsedLookupABI(t *testing.T) ABI {
	t.Helper()
	a, err := JSON(strings.NewReader(lookupABI))
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}
	return a
}

func TestMethodByIdFound(t *testing.T) {
	a := parsedLookupABI(t)
	method := a.Methods["foo"]
	got, err := a.MethodById(method.ID)
	if err != nil {
		t.Fatalf("MethodById() error: %v", err)
	}
	if got.Name != "foo" {
		t.Errorf("MethodById() = %q, want %q", got.Name, "foo")
	}
}

func TestMethodByIdErrors(t *testing.T) {
	a := parsedLookupABI(t)
	if _, err := a.MethodById([]byte{0x01, 0x02}); err == nil {
		t.Error("expected error for too-short sigdata")
	}
	if _, err := a.MethodById([]byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Error("expected error for unknown method id")
	}
}

func TestErrorByIDFoundAndMissing(t *testing.T) {
	a := parsedLookupABI(t)
	errAbi := a.Errors["CustomError"]
	var id [4]byte
	copy(id[:], errAbi.ID[:4])
	got, err := a.ErrorByID(id)
	if err != nil {
		t.Fatalf("ErrorByID() error: %v", err)
	}
	if got.Name != "CustomError" {
		t.Errorf("ErrorByID() = %q, want %q", got.Name, "CustomError")
	}

	var badID [4]byte
	copy(badID[:], []byte{0xFF, 0xFF, 0xFF, 0xFF})
	if _, err := a.ErrorByID(badID); err == nil {
		t.Error("expected error for unknown error id")
	}
}

func TestEventByIDFoundAndMissing(t *testing.T) {
	a := parsedLookupABI(t)
	ev := a.Events["Transfer"]
	got, err := a.EventByID(ev.ID)
	if err != nil {
		t.Fatalf("EventByID() error: %v", err)
	}
	if got.Name != "Transfer" {
		t.Errorf("EventByID() = %q, want %q", got.Name, "Transfer")
	}

	if _, err := a.EventByID(ev.ID); err != nil {
		t.Fatalf("unexpected error on second lookup: %v", err)
	}
	// Flip a byte to produce an unknown topic.
	badTopic := ev.ID
	badTopic[0] ^= 0xFF
	if _, err := a.EventByID(badTopic); err == nil {
		t.Error("expected error for unknown event id")
	}
}

func TestUnpackIntoMap(t *testing.T) {
	a := parsedLookupABI(t)

	v := make(map[string]interface{})
	// Outputs of "foo" is a single uint256 named "b"; build return data by
	// packing it directly through the Method.Outputs arguments.
	method := a.Methods["foo"]
	data, err := method.Outputs.Pack(big.NewInt(99))
	if err != nil {
		t.Fatalf("Outputs.Pack() error: %v", err)
	}
	if err := a.UnpackIntoMap(v, "foo", data); err != nil {
		t.Fatalf("UnpackIntoMap() error: %v", err)
	}
	got, ok := v["b"]
	if !ok {
		t.Fatalf("UnpackIntoMap() result missing key 'b': %#v", v)
	}
	gotBig, ok := got.(*big.Int)
	if !ok || gotBig.Cmp(big.NewInt(99)) != 0 {
		t.Errorf("UnpackIntoMap()['b'] = %v, want 99", got)
	}
}

func TestEventStringAndNewEvent(t *testing.T) {
	a := parsedLookupABI(t)
	ev := a.Events["Transfer"]
	s := ev.String()
	if !strings.Contains(s, "Transfer") {
		t.Errorf("Event.String() = %q, want it to contain %q", s, "Transfer")
	}
	if !strings.Contains(s, "indexed") {
		t.Errorf("Event.String() = %q, want it to mention 'indexed'", s)
	}
}

func TestErrorString(t *testing.T) {
	a := parsedLookupABI(t)
	errAbi := a.Errors["CustomError"]
	s := errAbi.String()
	if !strings.Contains(s, "CustomError") {
		t.Errorf("Error.String() = %q, want it to contain %q", s, "CustomError")
	}
}

func TestMethodIsConstantAndIsPayable(t *testing.T) {
	a := parsedLookupABI(t)
	foo := a.Methods["foo"]
	if !foo.IsConstant() {
		t.Error("foo (stateMutability=view) should be constant")
	}
	if foo.IsPayable() {
		t.Error("foo should not be payable")
	}

	bar := a.Methods["bar"]
	if bar.IsConstant() {
		t.Error("bar (stateMutability=payable) should not be constant")
	}
	if !bar.IsPayable() {
		t.Error("bar should be payable")
	}
}
