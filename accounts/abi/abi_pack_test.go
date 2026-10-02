package abi

import (
	"math/big"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/crypto"
)

const testABIJSON = `[
	{"type":"constructor","inputs":[{"name":"owner","type":"address"}]},
	{"type":"function","name":"add","inputs":[{"name":"a","type":"uint256"},{"name":"b","type":"uint256"}],"outputs":[{"name":"","type":"uint256"}],"stateMutability":"view"},
	{"type":"function","name":"noop","inputs":[],"outputs":[]},
	{"type":"event","name":"Added","inputs":[{"name":"a","type":"uint256","indexed":true},{"name":"sum","type":"uint256","indexed":false}],"anonymous":false},
	{"type":"error","name":"Oops","inputs":[{"name":"code","type":"uint256"}]},
	{"type":"fallback","stateMutability":"nonpayable"},
	{"type":"receive","stateMutability":"payable"}
]`

func mustParseABI(t *testing.T) ABI {
	t.Helper()
	a, err := JSON(strings.NewReader(testABIJSON))
	if err != nil {
		t.Fatalf("JSON parse error: %v", err)
	}
	return a
}

func TestABIJSONParsing(t *testing.T) {
	a := mustParseABI(t)
	if len(a.Methods) != 2 {
		t.Fatalf("expected 2 methods, got %d", len(a.Methods))
	}
	if len(a.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(a.Events))
	}
	if len(a.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(a.Errors))
	}
	if !a.HasFallback() {
		t.Error("expected fallback")
	}
	if !a.HasReceive() {
		t.Error("expected receive")
	}
}

func TestABIPackUnpack(t *testing.T) {
	a := mustParseABI(t)
	packed, err := a.Pack("add", big.NewInt(2), big.NewInt(3))
	if err != nil {
		t.Fatalf("Pack error: %v", err)
	}
	if len(packed) < 4 {
		t.Fatalf("packed data too short")
	}
	method, err := a.MethodById(packed[:4])
	if err != nil {
		t.Fatalf("MethodById error: %v", err)
	}
	if method.Name != "add" {
		t.Errorf("MethodById name = %q, want add", method.Name)
	}

	// pack noop (no args)
	packedNoop, err := a.Pack("noop")
	if err != nil {
		t.Fatalf("Pack(noop) error: %v", err)
	}
	if len(packedNoop) != 4 {
		t.Errorf("expected 4-byte selector-only packing, got %d bytes", len(packedNoop))
	}

	// constructor packing (name == "")
	ctorPacked, err := a.Pack("", mustAddressArg(t))
	if err != nil {
		t.Fatalf("Pack(constructor) error: %v", err)
	}
	if len(ctorPacked) != 32 {
		t.Errorf("expected 32-byte constructor packing, got %d", len(ctorPacked))
	}

	// unpack output of add() -- simulate a uint256 return value
	out, err := a.Unpack("add", packUint256(5))
	if err != nil {
		t.Fatalf("Unpack error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 unpacked value, got %d", len(out))
	}
	v, ok := out[0].(*big.Int)
	if !ok || v.Cmp(big.NewInt(5)) != 0 {
		t.Errorf("unpacked value = %v, want 5", out[0])
	}
}

func mustAddressArg(t *testing.T) [20]byte {
	t.Helper()
	var a [20]byte
	a[0] = 0xAB
	return a
}

func packUint256(v int64) []byte {
	ty, _ := NewType("uint256", "", nil)
	packed, _ := (Arguments{{Type: ty}}).Pack(big.NewInt(v))
	return packed
}

func TestABIPackUnknownMethod(t *testing.T) {
	a := mustParseABI(t)
	if _, err := a.Pack("doesnotexist"); err == nil {
		t.Error("expected error for unknown method")
	}
}

func TestABIUnpackUnknownName(t *testing.T) {
	a := mustParseABI(t)
	if _, err := a.Unpack("doesnotexist", nil); err == nil {
		t.Error("expected error for unknown method/event")
	}
}

func TestABIUnpackIntoMap(t *testing.T) {
	a := mustParseABI(t)
	out := map[string]interface{}{}
	if err := a.UnpackIntoMap(out, "add", packUint256(7)); err != nil {
		t.Fatalf("UnpackIntoMap error: %v", err)
	}
}

func TestABIErrorByIDCustom(t *testing.T) {
	a := mustParseABI(t)
	oops := a.Errors["Oops"]
	var id [4]byte
	copy(id[:], oops.ID[:4])
	got, err := a.ErrorByID(id)
	if err != nil {
		t.Fatalf("ErrorByID error: %v", err)
	}
	if got.Name != "Oops" {
		t.Errorf("got %q, want Oops", got.Name)
	}
	var bogus [4]byte
	bogus[0] = 0xFF
	bogus[1] = 0xFF
	bogus[2] = 0xFF
	bogus[3] = 0xFF
	if _, err := a.ErrorByID(bogus); err == nil {
		t.Error("expected error for unknown error id")
	}
}

func TestABIEventByID(t *testing.T) {
	a := mustParseABI(t)
	ev := a.Events["Added"]
	got, err := a.EventByID(ev.ID)
	if err != nil {
		t.Fatalf("EventByID error: %v", err)
	}
	if got.Name != "Added" {
		t.Errorf("got %q, want Added", got.Name)
	}
}

func TestABIMethodByIdTooShort(t *testing.T) {
	a := mustParseABI(t)
	if _, err := a.MethodById([]byte{0x01}); err == nil {
		t.Error("expected error for short sigdata")
	}
}

func TestABIDuplicateFallbackReceive(t *testing.T) {
	doubleFallback := `[{"type":"fallback"},{"type":"fallback"}]`
	if _, err := JSON(strings.NewReader(doubleFallback)); err == nil {
		t.Error("expected error for duplicate fallback")
	}
	doubleReceive := `[{"type":"receive","stateMutability":"payable"},{"type":"receive","stateMutability":"payable"}]`
	if _, err := JSON(strings.NewReader(doubleReceive)); err == nil {
		t.Error("expected error for duplicate receive")
	}
	badReceive := `[{"type":"receive","stateMutability":"nonpayable"}]`
	if _, err := JSON(strings.NewReader(badReceive)); err == nil {
		t.Error("expected error for non-payable receive")
	}
}

func TestABIUnknownFieldType(t *testing.T) {
	bad := `[{"type":"bogus"}]`
	if _, err := JSON(strings.NewReader(bad)); err == nil {
		t.Error("expected error for unrecognized field type")
	}
}

func TestUnpackRevertError(t *testing.T) {
	sel := crypto.Keccak256([]byte("Error(string)"))[:4]
	ty, _ := NewType("string", "", nil)
	packed, err := (Arguments{{Type: ty}}).Pack("bad input")
	if err != nil {
		t.Fatalf("Pack error: %v", err)
	}
	data := append(append([]byte{}, sel...), packed...)
	reason, err := UnpackRevert(data)
	if err != nil {
		t.Fatalf("UnpackRevert error: %v", err)
	}
	if reason != "bad input" {
		t.Errorf("reason = %q, want %q", reason, "bad input")
	}
}

func TestUnpackRevertPanic(t *testing.T) {
	sel := crypto.Keccak256([]byte("Panic(uint256)"))[:4]
	ty, _ := NewType("uint256", "", nil)
	packed, err := (Arguments{{Type: ty}}).Pack(big.NewInt(0x11))
	if err != nil {
		t.Fatalf("Pack error: %v", err)
	}
	data := append(append([]byte{}, sel...), packed...)
	reason, err := UnpackRevert(data)
	if err != nil {
		t.Fatalf("UnpackRevert error: %v", err)
	}
	if reason != "arithmetic underflow or overflow" {
		t.Errorf("reason = %q", reason)
	}
}

func TestUnpackRevertUnknownPanicCode(t *testing.T) {
	sel := crypto.Keccak256([]byte("Panic(uint256)"))[:4]
	ty, _ := NewType("uint256", "", nil)
	packed, _ := (Arguments{{Type: ty}}).Pack(big.NewInt(0xFF))
	data := append(append([]byte{}, sel...), packed...)
	reason, err := UnpackRevert(data)
	if err != nil {
		t.Fatalf("UnpackRevert error: %v", err)
	}
	if !strings.Contains(reason, "unknown panic code") {
		t.Errorf("reason = %q", reason)
	}
}

func TestUnpackRevertErrors(t *testing.T) {
	if _, err := UnpackRevert([]byte{0x01}); err == nil {
		t.Error("expected error for short data")
	}
	if _, err := UnpackRevert([]byte{0xde, 0xad, 0xbe, 0xef}); err == nil {
		t.Error("expected error for unknown selector")
	}
}
