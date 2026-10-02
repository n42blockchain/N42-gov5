package abi

import (
	"math/big"
	"testing"
)

func TestArgumentsUnpackEmptyDataErrors(t *testing.T) {
	args := Arguments{{Name: "a", Type: mustNewType("uint256")}}
	if _, err := args.Unpack(nil); err == nil {
		t.Error("expected error unpacking empty data when arguments are expected")
	}

	empty := Arguments{}
	out, err := empty.Unpack(nil)
	if err != nil {
		t.Fatalf("Unpack() on no-argument set with empty data should not error: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("Unpack() = %#v, want empty slice", out)
	}
}

func TestArgumentsPackValues(t *testing.T) {
	args := Arguments{{Name: "a", Type: mustNewType("uint256")}}
	packed, err := args.PackValues([]interface{}{big.NewInt(7)})
	if err != nil {
		t.Fatalf("PackValues() error: %v", err)
	}
	unpacked, err := args.Unpack(packed)
	if err != nil {
		t.Fatalf("Unpack() error: %v", err)
	}
	got, ok := unpacked[0].(*big.Int)
	if !ok || got.Cmp(big.NewInt(7)) != 0 {
		t.Errorf("round trip mismatch: got %v, want 7", unpacked[0])
	}
}

func TestArgumentsCopyNonPointer(t *testing.T) {
	args := Arguments{{Name: "a", Type: mustNewType("uint256")}}
	var dst int
	if err := args.Copy(dst, []interface{}{big.NewInt(1)}); err == nil {
		t.Error("expected error when copying into a non-pointer")
	}
}

func TestArgumentsCopyEmptyValues(t *testing.T) {
	empty := Arguments{}
	var dst struct{}
	if err := empty.Copy(&dst, nil); err != nil {
		t.Errorf("Copy() with no arguments/values should not error: %v", err)
	}

	nonEmpty := Arguments{{Name: "a", Type: mustNewType("uint256")}}
	var dst2 big.Int
	if err := nonEmpty.Copy(&dst2, nil); err == nil {
		t.Error("expected error copying zero values when arguments are expected")
	}
}

func TestArgumentsCopyAtomic(t *testing.T) {
	args := Arguments{{Name: "a", Type: mustNewType("uint256")}}
	var dst *big.Int
	if err := args.Copy(&dst, []interface{}{big.NewInt(42)}); err != nil {
		t.Fatalf("Copy() error: %v", err)
	}
	if dst == nil || dst.Cmp(big.NewInt(42)) != 0 {
		t.Errorf("Copy() = %v, want 42", dst)
	}
}

func TestArgumentsUnpackIntoMapEmptyData(t *testing.T) {
	empty := Arguments{}
	v := make(map[string]interface{})
	if err := empty.UnpackIntoMap(v, nil); err != nil {
		t.Errorf("UnpackIntoMap() with no arguments/empty data should not error: %v", err)
	}

	nonEmpty := Arguments{{Name: "a", Type: mustNewType("uint256")}}
	if err := nonEmpty.UnpackIntoMap(v, nil); err == nil {
		t.Error("expected error unpacking empty data into map when arguments are expected")
	}
}

func TestArgumentsUnpackIntoMapNilMap(t *testing.T) {
	args := Arguments{{Name: "a", Type: mustNewType("uint256")}}
	data, err := args.Pack(big.NewInt(1))
	if err != nil {
		t.Fatalf("Pack() error: %v", err)
	}
	if err := args.UnpackIntoMap(nil, data); err == nil {
		t.Error("expected error unpacking into a nil map")
	}
}

func TestErrorUnpack(t *testing.T) {
	e := NewError("Custom", Arguments{{Name: "code", Type: mustNewType("uint256")}})
	payload, err := e.Inputs.Pack(big.NewInt(5))
	if err != nil {
		t.Fatalf("Pack() error: %v", err)
	}
	raw := append(append([]byte{}, e.ID[:4]...), payload...)

	got, err := e.Unpack(raw)
	if err != nil {
		t.Fatalf("Unpack() error: %v", err)
	}
	values, ok := got.([]interface{})
	if !ok || len(values) != 1 {
		t.Fatalf("Unpack() = %#v", got)
	}

	if _, err := e.Unpack([]byte{0x01}); err == nil {
		t.Error("expected error unpacking too-short data")
	}
	if _, err := e.Unpack([]byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Error("expected error unpacking data with mismatched selector")
	}
}
