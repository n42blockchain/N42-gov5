package abi

import (
	"math/big"
	"strings"
	"testing"
)

func TestABIPackConstructorAndErrors(t *testing.T) {
	a, err := JSON(strings.NewReader(`[{"type":"constructor","inputs":[{"name":"x","type":"uint256"}]}]`))
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}
	packed, err := a.Pack("", big.NewInt(5))
	if err != nil {
		t.Fatalf("Pack(constructor) error: %v", err)
	}
	if len(packed) == 0 {
		t.Fatal("Pack(constructor) returned empty output")
	}

	if _, err := a.Pack("doesNotExist"); err == nil {
		t.Error("expected error packing an unknown method name")
	}
}

func TestABIGetArgumentsErrors(t *testing.T) {
	a, err := JSON(strings.NewReader(`[{"type":"function","name":"foo","inputs":[],"outputs":[{"name":"b","type":"uint256"}]}]`))
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}
	if _, err := a.Unpack("foo", []byte{0x01, 0x02, 0x03}); err == nil {
		t.Error("expected error for improperly formatted (non-32-byte-multiple) output")
	}
	if _, err := a.Unpack("doesNotExist", nil); err == nil {
		t.Error("expected error for unknown method/event name")
	}
}

func TestABIUnpackIntoInterface(t *testing.T) {
	a, err := JSON(strings.NewReader(`[{"type":"function","name":"foo","inputs":[],"outputs":[{"name":"b","type":"uint256"}]}]`))
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}
	data, err := a.Methods["foo"].Outputs.Pack(big.NewInt(7))
	if err != nil {
		t.Fatalf("Outputs.Pack() error: %v", err)
	}
	var out struct {
		B *big.Int
	}
	if err := a.UnpackIntoInterface(&out, "foo", data); err != nil {
		t.Fatalf("UnpackIntoInterface() error: %v", err)
	}
	if out.B == nil || out.B.Cmp(big.NewInt(7)) != 0 {
		t.Errorf("UnpackIntoInterface() = %v, want 7", out.B)
	}
}

func TestABIUnmarshalJSONDuplicateFallbackAndReceive(t *testing.T) {
	if _, err := JSON(strings.NewReader(`[{"type":"fallback","stateMutability":"nonpayable"},{"type":"fallback","stateMutability":"nonpayable"}]`)); err == nil {
		t.Error("expected error for duplicate fallback")
	}
	if _, err := JSON(strings.NewReader(`[{"type":"receive","stateMutability":"payable"},{"type":"receive","stateMutability":"payable"}]`)); err == nil {
		t.Error("expected error for duplicate receive")
	}
	if _, err := JSON(strings.NewReader(`[{"type":"receive","stateMutability":"nonpayable"}]`)); err == nil {
		t.Error("expected error for a non-payable receive function")
	}
	if _, err := JSON(strings.NewReader(`[{"type":"bogus"}]`)); err == nil {
		t.Error("expected error for an unrecognized ABI field type")
	}
	if _, err := JSON(strings.NewReader(`not json`)); err == nil {
		t.Error("expected error for malformed JSON input")
	}
}

func TestABIHasFallbackAndReceive(t *testing.T) {
	a, err := JSON(strings.NewReader(`[{"type":"fallback","stateMutability":"nonpayable"},{"type":"receive","stateMutability":"payable"}]`))
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}
	if !a.HasFallback() {
		t.Error("HasFallback() = false, want true")
	}
	if !a.HasReceive() {
		t.Error("HasReceive() = false, want true")
	}

	empty := ABI{}
	if empty.HasFallback() {
		t.Error("HasFallback() on empty ABI = true, want false")
	}
	if empty.HasReceive() {
		t.Error("HasReceive() on empty ABI = true, want false")
	}
}
